// museframe-assets —— 资产迁移与逐一校验工具。
//
// 做四件事：
//  1. 把源资产目录（**必须是备份出来的副本**，绝不指向生产 /opt/museframe/data/assets）
//     复制到统一持久化目录；
//  2. 对每个文件现算 sha256（🔴 源库 assets.sha256 列全为 NULL，从未被写入，
//     内容级校验不能依赖库里已有的值）并回填 assets.sha256；
//  3. 与 DB 的 assets.storage_key 逐条交叉校验；
//  4. 打印四个数字：引用总数 / 磁盘命中 / 孤儿引用 / 孤儿文件。
//     迁移验收基线（盘点 B7）：19 / 19 / 0 / 0，且字节和 = 10970358。
//
// 用法：
//
//	museframe-assets -src <备份资产目录> -dst <目标资产目录> [-apply] [-backfill-sha256]
//
// 不带 -apply 是**演练**：只校验不复制、不写库。
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"museframe-api/internal/store"
)

func main() {
	var (
		src          = flag.String("src", "", "源资产目录（备份副本，只读）")
		dst          = flag.String("dst", "", "目标资产目录")
		apply        = flag.Bool("apply", false, "真正复制文件（缺省只演练）")
		backfill     = flag.Bool("backfill-sha256", false, "把现算的 sha256 回填进 assets.sha256")
		dsn          = flag.String("dsn", os.Getenv("MUSEFRAME_DATABASE_URL"), "PostgreSQL 连接串（迁移用 owner 角色）")
		allowOrphans = flag.Bool("allow-orphans", false, "允许存在孤儿引用 / 孤儿文件（默认不允许，直接失败）")
	)
	flag.Parse()
	if *src == "" || *dsn == "" {
		fmt.Fprintln(os.Stderr, "用法：museframe-assets -src <目录> -dst <目录> -dsn <连接串> [-apply] [-backfill-sha256]")
		os.Exit(2)
	}
	if strings.HasPrefix(filepath.Clean(*src), "/opt/museframe/data/assets") {
		fmt.Fprintln(os.Stderr, "拒绝执行：-src 必须是备份出来的副本，不能指向生产资产目录")
		os.Exit(2)
	}

	ctx := context.Background()
	st, err := store.Open(ctx, *dsn, 2)
	if err != nil {
		fmt.Fprintln(os.Stderr, "连接数据库失败")
		os.Exit(1)
	}
	defer st.Close()

	refs, err := listRefs(ctx, st)
	if err != nil {
		fmt.Fprintln(os.Stderr, "读取 assets 引用失败:", err)
		os.Exit(1)
	}
	onDisk, err := listFiles(*src)
	if err != nil {
		fmt.Fprintln(os.Stderr, "扫描源目录失败:", err)
		os.Exit(1)
	}

	var hits, missing []string
	var diskBytes, dbBytes int64
	sums := map[string]string{}
	for _, r := range refs {
		dbBytes += r.byteSize
		p := filepath.Join(*src, r.storageKey)
		fi, err := os.Stat(p)
		if err != nil || !fi.Mode().IsRegular() {
			missing = append(missing, r.storageKey)
			continue
		}
		sum, err := sha256File(p)
		if err != nil {
			fmt.Fprintf(os.Stderr, "读取 %s 失败: %v\n", r.storageKey, err)
			os.Exit(1)
		}
		sums[r.id] = sum
		diskBytes += fi.Size()
		hits = append(hits, r.storageKey)
		if fi.Size() != r.byteSize {
			fmt.Fprintf(os.Stderr, "【红】%s 的磁盘字节数 %d 与 DB 记录 %d 不一致\n", r.storageKey, fi.Size(), r.byteSize)
		}
	}

	referenced := map[string]bool{}
	for _, r := range refs {
		referenced[r.storageKey] = true
	}
	var orphanFiles []string
	for _, f := range onDisk {
		if !referenced[f] {
			orphanFiles = append(orphanFiles, f)
		}
	}
	sort.Strings(missing)
	sort.Strings(orphanFiles)

	fmt.Println("== 资产交叉校验 ==")
	fmt.Printf("引用总数（SELECT storage_key FROM assets）= %d\n", len(refs))
	fmt.Printf("磁盘命中数                                = %d\n", len(hits))
	fmt.Printf("孤儿引用（DB 有记录、文件缺失）            = %d\n", len(missing))
	fmt.Printf("孤儿文件（磁盘有、DB 无引用）              = %d\n", len(orphanFiles))
	fmt.Printf("DB sum(byte_size)                        = %d\n", dbBytes)
	fmt.Printf("磁盘实测字节和                            = %d\n", diskBytes)
	if len(missing) > 0 {
		fmt.Println("孤儿引用清单:", strings.Join(missing, ", "))
	}
	if len(orphanFiles) > 0 {
		fmt.Println("孤儿文件清单:", strings.Join(orphanFiles, ", "))
	}
	if dbBytes != diskBytes {
		fmt.Fprintln(os.Stderr, "【红】字节和不一致，迁移必须停下")
		os.Exit(1)
	}
	// 🔴 验收基线是「孤儿引用 0 / 孤儿文件 0」。有任何一个就停下，
	// 不要让「校验跑过了」变成一句空话。
	if !*allowOrphans && (len(missing) > 0 || len(orphanFiles) > 0) {
		fmt.Fprintf(os.Stderr, "【红】孤儿引用 %d / 孤儿文件 %d，不满足验收基线（0/0），迁移停下\n",
			len(missing), len(orphanFiles))
		os.Exit(1)
	}

	if *apply {
		if *dst == "" {
			fmt.Fprintln(os.Stderr, "-apply 需要 -dst")
			os.Exit(2)
		}
		if err := os.MkdirAll(*dst, 0o750); err != nil {
			fmt.Fprintln(os.Stderr, "创建目标目录失败:", err)
			os.Exit(1)
		}
		copied := 0
		for _, key := range hits {
			if err := copyVerified(filepath.Join(*src, key), filepath.Join(*dst, key)); err != nil {
				fmt.Fprintf(os.Stderr, "复制 %s 失败: %v\n", key, err)
				os.Exit(1)
			}
			copied++
		}
		fmt.Printf("已复制 %d 个文件到 %s（逐个复算 sha256 双边比对通过）\n", copied, *dst)
	}

	if *backfill {
		n := 0
		for id, sum := range sums {
			if _, err := st.Pool().Exec(ctx,
				`UPDATE assets SET sha256 = $1, updated_at = updated_at WHERE id = $2 AND sha256 IS NULL`, sum, id); err != nil {
				fmt.Fprintln(os.Stderr, "回填 sha256 失败:", err)
				os.Exit(1)
			}
			n++
		}
		fmt.Printf("已回填 sha256：%d 行（源库该列全为 NULL，值是本次现算的）\n", n)
	}
	_ = time.Now
}

type assetRef struct {
	id         string
	storageKey string
	byteSize   int64
}

func listRefs(ctx context.Context, st *store.Store) ([]assetRef, error) {
	rows, err := st.Pool().Query(ctx,
		`SELECT id, storage_key, COALESCE(byte_size,0) FROM assets WHERE deleted_at IS NULL ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []assetRef
	for rows.Next() {
		var r assetRef
		if err := rows.Scan(&r.id, &r.storageKey, &r.byteSize); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// listFiles 列源目录下的文件（单层平铺，文件名 = storage_key）。
func listFiles(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if e.Type().IsRegular() {
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	return out, nil
}

func sha256File(p string) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// copyVerified 复制后复算 sha256 双边比对 —— 内容级校验不能靠库里已有的值。
func copyVerified(src, dst string) error {
	want, err := sha256File(src)
	if err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o640)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	got, err := sha256File(dst)
	if err != nil {
		return err
	}
	if got != want {
		return fmt.Errorf("复制后 sha256 不一致")
	}
	return nil
}
