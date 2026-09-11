package store

import "time"

// User 对应 users 表。is_guest 在 PG 里是 boolean（SQLite 是 INTEGER 0/1）。
type User struct {
	ID          string
	Status      string
	IsGuest     bool
	DisplayName *string
	Locale      string
	Timezone    string
	CreatedAt   time.Time
	UpdatedAt   time.Time
	DeletedAt   *time.Time
}

// Session 对应 sessions 表。令牌是不透明随机串，明文主键（与 Node 版一致）。
type Session struct {
	Token      string
	UserID     string
	DeviceID   *string
	CreatedAt  time.Time
	LastSeenAt time.Time
	ExpiresAt  *time.Time
}

// Asset 对应 assets 表。
type Asset struct {
	ID          string
	UserID      string
	ProjectID   *string
	Kind        string
	Status      string
	StorageKey  string
	ContentType string
	ByteSize    *int64
	Width       *int
	Height      *int
	SHA256      *string
	CreatedAt   time.Time
	UpdatedAt   time.Time
	DeletedAt   *time.Time
}

// Project 对应 projects 表。status: draft | generating | ready | saved。
type Project struct {
	ID                  string
	UserID              string
	Title               *string
	SourceAssetID       *string
	SelectedCandidateID *string
	Status              string
	CreatedAt           time.Time
	UpdatedAt           time.Time
	DeletedAt           *time.Time
}

// Job 对应 generation_jobs 表。
// status 7 值：created|queued|running|quality_check|succeeded|failed|cancelled
// stage  6 值：preparing|building|making|checking|complete|failed
// 注意：cancelled 的 stage 被写成 failed（与 Node 版一致，不是 cancelled）。
type Job struct {
	ID             string
	UserID         string
	ProjectID      string
	SourceAssetID  string
	StyleVersionID string
	ParentJobID    *string
	Status         string
	Stage          string
	Controls       []byte
	Output         []byte
	AttemptCount   int
	ReservedUnits  int
	ErrorCode      *string
	CostMinor      int64
	CreatedAt      time.Time
	UpdatedAt      time.Time
	FinishedAt     *time.Time
}

// Candidate 对应 generation_candidates 表。
type Candidate struct {
	ID            string
	JobID         string
	Index         int
	AssetID       string
	QualityPassed bool
	CreatedAt     time.Time
}

// Product 对应 products 表。
//
// 🔴 PriceMinor 是**美分**，PriceCnyMinor 是**人民币分**且可为 NULL。
// Currency 恒为 USD 且**只描述 PriceMinor** —— PriceCnyMinor 没有对应的币种列。
type Product struct {
	ID              string
	InternalKey     string
	ProductType     string
	DisplayName     string
	GrantedUnits    int
	PriceMinor      int64
	Currency        string
	Period          *string
	FeatureFlags    []byte
	Active          bool
	GoogleProductID *string
	AppleProductID  *string
	PriceCnyMinor   *int64
}

// Purchase 对应 purchases 表。
type Purchase struct {
	ID                    string
	UserID                string
	ProductID             string
	Platform              string
	ExternalTransactionID string
	Status                string
	AmountMinor           *int64
	Currency              *string
	PurchasedAt           time.Time
	ExpiresAt             *time.Time
	CreatedAt             time.Time
}

// StyleRow 是 publishedStyleRows() 的一行：风格 + 最新已发布版本 + 所属展览排序。
type StyleRow struct {
	StyleID         string
	InternalKey     string
	Premium         bool
	PublicName      string
	ShortCaption    string
	SuitabilityTags []byte
	Theme           string
	VersionID       string
	Version         int
	Spec            []byte
	EditorialRank   int
	ExhibitionID    string
	Position        int
}

// Exhibition 对应 exhibitions 表。
type Exhibition struct {
	ID             string
	Slug           string
	Title          string
	CuratorialNote string
	Edition        string
	EditorialRank  int
	Status         string
	CreatedAt      time.Time
}

// PhotoAnalysis 对应 photo_analyses 表。
type PhotoAnalysis struct {
	ID              string
	AssetID         string
	AnalyzerVersion string
	Status          string
	SubjectType     *string
	PersonCount     *int
	Sharpness       *float64
	Exposure        *float64
	Warnings        []byte
	Recommendations []byte
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// EmailCode 对应 email_codes 表。
type EmailCode struct {
	Email       string
	CodeHash    string
	ExpiresAt   time.Time
	Attempts    int
	CreatedAt   time.Time
	IssueCount  int
	WindowStart *time.Time
}

// IdempotencyRecord 对应 idempotency_records 表。
type IdempotencyRecord struct {
	UserID         string
	Key            string
	RequestHash    string
	ResponseStatus *int
	ResponseBody   []byte
	CreatedAt      time.Time
}

// Bucket 是 credit_buckets 的一行加上它的台账净额（bucketBalances 的投影）。
type Bucket struct {
	ID        string
	ExpiresAt *time.Time
	CreatedAt time.Time
	Balance   int
}

// FreeGrantWindow 是滚动 24 小时的免费发放计数。
// 三个计数**一律带 units > 0**：占位行不能把上限吃光。
type FreeGrantWindow struct {
	Today int
	ForIP int
	IPs   int
}
