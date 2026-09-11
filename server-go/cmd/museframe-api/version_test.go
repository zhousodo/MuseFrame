package main

import "testing"

// 🔴 这组断言是为了一个**真实发生过的**缺陷：2026-09-12 验收时后台「运行状态 ·
// 后端版本」显示 "dev"，而线上镜像 tag 是 20260911T194643Z-g5de73601 —— 本地交叉
// 编译那一步的 -X main.version 没生效。所以这里同时钉住两件事：
//  1. ldflags 注入到了就必须用注入值（别被环境变量盖掉）；
//  2. 注入漏了（空串 / 还是占位 "dev"）就必须回退成镜像 tag，而不是把 "dev" 展示给运维。
func TestResolveVersion(t *testing.T) {
	env := func(m map[string]string) func(string) string {
		return func(k string) string { return m[k] }
	}
	cases := []struct {
		name     string
		injected string
		env      map[string]string
		want     string
	}{
		{"注入值优先于环境变量", "20260912T000000Z-gdeadbeef",
			map[string]string{"IMAGE_TAG": "20260911T194643Z-g5de73601"}, "20260912T000000Z-gdeadbeef"},
		{"注入漏了回退 IMAGE_TAG", "dev",
			map[string]string{"IMAGE_TAG": "20260911T194643Z-g5de73601"}, "20260911T194643Z-g5de73601"},
		{"空注入回退 IMAGE_TAG", "",
			map[string]string{"IMAGE_TAG": "20260911T194643Z-g5de73601"}, "20260911T194643Z-g5de73601"},
		{"MUSEFRAME_IMAGE_TAG 优先于 IMAGE_TAG", "dev",
			map[string]string{"MUSEFRAME_IMAGE_TAG": "a-tag", "IMAGE_TAG": "b-tag"}, "a-tag"},
		{"环境变量只有空白等于没设", "dev",
			map[string]string{"IMAGE_TAG": "   "}, "dev"},
		{"两路都没有才显示 dev", "dev", map[string]string{}, "dev"},
		{"注入值两端空白被裁掉", "  tag-with-space  ", map[string]string{}, "tag-with-space"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := resolveVersion(tc.injected, env(tc.env)); got != tc.want {
				t.Fatalf("resolveVersion(%q, %v) = %q，应为 %q", tc.injected, tc.env, got, tc.want)
			}
		})
	}
}
