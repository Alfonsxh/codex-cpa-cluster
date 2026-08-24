package quota

const (
	WeeklyWindowSeconds = int64(7 * 24 * 60 * 60)
	RuntimeStateName    = "official_quota"
	runtimeStateVersion = 1
)

type OAuthRecord struct {
	AccessToken string `json:"-"`
	AccountID   string `json:"-"`
}

type WeeklyWindow struct {
	Key                 string  `json:"key"`
	Label               string  `json:"label"`
	MeteredFeature      *string `json:"metered_feature"`
	WindowSlot          string  `json:"window_slot"`
	UsedPercent         float64 `json:"used_percent"`
	RemainingPercent    float64 `json:"remaining_percent"`
	ReportedUsedPercent float64 `json:"reported_used_percent"`
	ResetAt             *int64  `json:"reset_at"`
	ResetAfterSeconds   *int64  `json:"reset_after_seconds"`
	WindowSeconds       int64   `json:"window_seconds"`
	LimitReached        bool    `json:"limit_reached"`
	Resettable          bool    `json:"resettable"`
}

type AccountQuota struct {
	Account          string         `json:"account"`
	Status           string         `json:"status"`
	PlanType         *string        `json:"plan_type"`
	Allowed          *bool          `json:"allowed"`
	LimitReached     *bool          `json:"limit_reached"`
	ResetCreditCount *int64         `json:"reset_credit_count"`
	Weekly           *WeeklyWindow  `json:"weekly"`
	WeeklyWindows    []WeeklyWindow `json:"weekly_windows"`
}

type Snapshot struct {
	GeneratedAt     int64          `json:"generated_at"`
	CacheTTLSeconds int64          `json:"cache_ttl_seconds"`
	Cached          bool           `json:"cached"`
	Refreshing      bool           `json:"refreshing"`
	Accounts        []AccountQuota `json:"accounts"`
}

type RuntimeState struct {
	Version       int      `json:"version"`
	HeartbeatAt   int64    `json:"heartbeat_at"`
	LastSuccessAt int64    `json:"last_success_at"`
	LastError     string   `json:"last_error"`
	Snapshot      Snapshot `json:"snapshot"`
}

func unavailableAccount(account string, status string) AccountQuota {
	return AccountQuota{
		Account: account, Status: status, WeeklyWindows: make([]WeeklyWindow, 0),
	}
}
