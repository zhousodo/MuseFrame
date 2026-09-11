package httpapi

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"regexp"

	"museframe-api/internal/apierr"
	"museframe-api/internal/ledger"
	"museframe-api/internal/store"
)

var idemKeyRe = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)

// JobCreated 是 POST /v1/generation-jobs 的出参。
type JobCreated struct {
	Job                 JobBrief            `json:"job"`
	EntitlementSnapshot EntitlementSnapshot `json:"entitlementSnapshot"`
}

// JobBrief 是新建任务的摘要。
type JobBrief struct {
	ID                    string `json:"id"`
	Status                string `json:"status"`
	Stage                 string `json:"stage"`
	EstimatedRangeSeconds []int  `json:"estimatedRangeSeconds"`
	ReservedUnits         int    `json:"reservedUnits"`
	CreatedAt             string `json:"createdAt"`
}

// EntitlementSnapshot 是建任务后的额度快照。
type EntitlementSnapshot struct {
	AvailableUnits int    `json:"availableUnits"`
	Plan           string `json:"plan"`
}

// hCreateJob 是 POST /v1/generation-jobs —— **计费入口**。
//
// 10 步按序（§6.6.2），少一步就是一条白嫖通道：
//
//	1  requireAccount            游客令牌一律 401
//	2  Idempotency-Key 必填 + request_hash 比对 + 响应回放（不重复计费）
//	3  generationStatus 不可用 -> 503，且**不扣额度**
//	4  project / asset / style_version 的归属与状态校验（归属下推到 SQL）
//	5  premium 风格 + free 计划 -> 402 {premiumStyle:true}
//	6  coerceControls 夹到 StyleSpec 白名单（同时堵住提示词注入）
//	7  qualityTier=high 仅 creator 计划可得，否则**强制降级**而不是报错
//	8  余额 < 1 -> 402 {requiredUnits, availableUnits}
//	9  🔴 单事务：INSERT job + reserveUnits + UPDATE projects.status
//	   （曾经分两次提交，留下「行已建、预留失败」的白嫖任务）
//	10 成功后写 idempotency_records
func (a *App) hCreateJob(c *Ctx) (any, error) {
	ctx := c.R.Context()
	u, err := requireAccount(c) // 1
	if err != nil {
		return nil, err
	}

	// 2
	idemKey := c.R.Header.Get("Idempotency-Key")
	if idemKey == "" {
		return nil, apierr.New(400, apierr.CodeIdempotencyKeyRequired, "Idempotency-Key header is required.")
	}
	if !idemKeyRe.MatchString(idemKey) {
		return nil, apierr.New(422, apierr.CodeValidation, "Idempotency-Key is invalid.")
	}
	canonical, err := json.Marshal(c.Body)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(canonical)
	reqHash := hex.EncodeToString(sum[:])
	if prior, err := store.GetIdempotency(ctx, a.st.Q(), u.ID, idemKey); err == nil {
		if prior.RequestHash != reqHash {
			return nil, apierr.New(409, apierr.CodeIdempotencyConflict, "Key was used with a different request.")
		}
		// 🔴 解进**具名结构体**再回放，而不是 map/any。
		// response_body 在 PG 里是 jsonb，jsonb 不保留键序（SQLite 存的是 TEXT，
		// 原样回放）。解进结构体后由 Go 按字段顺序重新序列化，回放响应与首次
		// 响应逐字节一致 —— 这正是「行数相等但字节不同」的典型翻车位。
		var replay JobCreated
		if err := json.Unmarshal(prior.ResponseBody, &replay); err != nil {
			return nil, err
		}
		return replay, nil
	} else if !store.IsNoRows(err) {
		return nil, err
	}

	// 3 —— 在写任何东西、扣任何额度之前先拒。
	if gen := a.prov.Status(); !gen.Available {
		return nil, apierr.New(503, apierr.CodeGenerationUnavailable, "Image generation is unavailable right now.")
	}

	projectID, err := requiredString(c.Body, "projectId", 100)
	if err != nil {
		return nil, err
	}
	sourceAssetID, err := requiredString(c.Body, "sourceAssetId", 100)
	if err != nil {
		return nil, err
	}
	styleVersionID, err := requiredString(c.Body, "styleVersionId", 100)
	if err != nil {
		return nil, err
	}
	parentJobID, err := optionalString(c.Body, "parentJobId", 100)
	if err != nil {
		return nil, err
	}
	controls, err := optionalObject(c.Body, "controls")
	if err != nil {
		return nil, err
	}
	output, err := optionalObject(c.Body, "output")
	if err != nil {
		return nil, err
	}

	// 4
	if _, err := store.GetProjectOfUser(ctx, a.st.Q(), projectID, u.ID); err != nil {
		if store.IsNoRows(err) {
			return nil, notFound("Project not found.")
		}
		return nil, err
	}
	ok, err := store.SourceReadyExists(ctx, a.st.Q(), sourceAssetID, u.ID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, apierr.New(409, apierr.CodeAssetNotReady, "Source photo is not ready.")
	}
	// s.status 和 v.status 一样重要：紧急下架把 styles.status 置 disabled，
	// 没有这个谓词，任何持有缓存 styleVersionId 的客户端还能继续用被撤下的风格生成。
	specRaw, premium, err := store.GetPublishedStyleVersion(ctx, a.st.Q(), styleVersionID)
	if err != nil {
		if store.IsNoRows(err) {
			return nil, apierr.New(404, apierr.CodeStyleUnavailable, "This direction is not available.")
		}
		return nil, err
	}

	plan, err := store.UserPlan(ctx, a.st.Q(), u.ID, a.now())
	if err != nil {
		return nil, err
	}
	// 5
	if premium && plan == "free" {
		return nil, apierr.WithDetails(402, apierr.CodeInsufficientEntitle,
			"A Creator plan is required for this direction.", map[string]any{"premiumStyle": true})
	}

	// 6 —— StyleSpec 声明了每个控制项的白名单。它一度从未被强制执行，
	// 而对三个走编译器的风格，这些字符串会被直接插进提示词编译器的用户回合里，
	// 也就是针对运维自己付费的供应商做提示词注入。不在白名单上的一律变默认值。
	spec, err := parseSpec(specRaw)
	if err != nil {
		return nil, err
	}
	safeControls := CoerceControls(spec, controls)

	// 7 —— aspectRatio 会进提示词与裁剪步骤，qualityTier 会回显给客户端，
	// 两者原本都逐字取自请求。high 档是 Creator 权益；别人要它就静悄悄给标准档，
	// 而不是报错。
	aspect, _ := output["aspectRatio"].(string)
	if !contains(AspectRatios, aspect) {
		aspect = "original"
	}
	tier := "standard"
	if q, _ := output["qualityTier"].(string); q == "high" && len(plan) >= 7 && plan[:7] == "creator" {
		tier = "high"
	}
	safeOutput := map[string]string{"aspectRatio": aspect, "qualityTier": tier}

	const units = 1
	// 8 —— 便宜的预检，免得付费墙弹回时在项目里留下一堆失败行；
	// 下面的 Reserve 才是有竞态保证的权威闸。
	balance, err := store.AvailableUnits(ctx, a.st.Q(), u.ID, a.now())
	if err != nil {
		return nil, err
	}
	if balance < units {
		return nil, apierr.WithDetails(402, apierr.CodeInsufficientEntitle, "A standard image is required.",
			map[string]any{"requiredUnits": units, "availableUnits": balance})
	}

	jobID := a.newID()
	t := a.now()
	controlsJSON, _ := json.Marshal(safeControls)
	outputJSON, _ := json.Marshal(safeOutput)

	// 9 —— 一个事务同时装下任务行和它的预留。
	// 它们曾经分别提交，崩溃卡在中间会留下两种坏状态：一条没有台账的 created 任务
	// （开机恢复把它重排队，白送一张图），以及一次失败的预留需要补偿性 UPDATE，
	// 而那条 UPDATE 自己也可能丢。
	// 🔴 幂等记录必须和建任务/扣额度在**同一个事务**里。
	// 早先它在事务之后、用连接池单独写：两个带同一个 Idempotency-Key 的并发请求
	// 会双双在上面第 2 步查不到记录，于是各自建一个任务、各自预留一份额度、
	// 各自入队 —— 一次用户操作扣两份额度、出两张图；随后慢的那个在写幂等记录时
	// 撞 PRIMARY KEY (user_id, idempotency_key) 拿到 500，而它那份额度已经花掉了。
	// 放进同一个事务后，这个主键自己就是串行化点：慢的那个整笔回滚
	// （任务、预留、项目状态一起没），再去回放快的那个的响应。
	var body JobCreated
	err = a.st.InTx(ctx, func(q store.Queryer) error {
		if err := store.InsertJob(ctx, q, &store.Job{
			ID: jobID, UserID: u.ID, ProjectID: projectID, SourceAssetID: sourceAssetID,
			StyleVersionID: styleVersionID, ParentJobID: parentJobID,
			Status: "queued", Stage: "preparing", Controls: controlsJSON, Output: outputJSON,
			ReservedUnits: units, CreatedAt: t, UpdatedAt: t,
		}); err != nil {
			return err
		}
		if err := ledger.Reserve(ctx, q, a.newID, u.ID, jobID, units, t); err != nil {
			return err
		}
		if err := store.SetProjectStatus(ctx, q, projectID, "generating", t); err != nil {
			return err
		}
		// 余额快照也挪进事务内读：出参里的 availableUnits 必须是扣完这一笔之后的值，
		// 在事务外读会把并发的其他扣减也算进来，回放出去的字节就不稳定了。
		after, err := store.AvailableUnits(ctx, q, u.ID, a.now())
		if err != nil {
			return err
		}
		body = JobCreated{
			Job: JobBrief{
				ID: jobID, Status: "queued", Stage: "preparing",
				EstimatedRangeSeconds: a.generationInfo().EstimatedRangeSeconds,
				ReservedUnits:         units, CreatedAt: store.ISO(t),
			},
			EntitlementSnapshot: EntitlementSnapshot{AvailableUnits: after, Plan: plan},
		}
		// 10
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		return store.InsertIdempotency(ctx, q, u.ID, idemKey, reqHash, 200, raw, t)
	})
	if err != nil {
		// 什么都没写：整个事务回滚了，不存在需要清理的孤儿任务行。
		if ins, ok := ledger.IsInsufficient(err); ok {
			return nil, apierr.WithDetails(402, apierr.CodeInsufficientEntitle, ins.Error(),
				map[string]any{"requiredUnits": ins.RequiredUnits, "availableUnits": ins.AvailableUnits})
		}
		// 输掉幂等键竞争：对方已经提交，本次整笔回滚（没扣额度、没建任务）。
		// 按契约回放对方的响应，而不是把竞态暴露成 500。
		if store.IsUniqueViolation(err) {
			prior, perr := store.GetIdempotency(ctx, a.st.Q(), u.ID, idemKey)
			if perr != nil {
				return nil, err
			}
			if prior.RequestHash != reqHash {
				return nil, apierr.New(409, apierr.CodeIdempotencyConflict, "Key was used with a different request.")
			}
			var replay JobCreated
			if uerr := json.Unmarshal(prior.ResponseBody, &replay); uerr != nil {
				return nil, uerr
			}
			return replay, nil
		}
		return nil, err
	}
	// 只有事务真的提交了才入队 —— 回滚的那一笔没有任务行可跑。
	a.worker.Enqueue(jobID)
	return body, nil
}
