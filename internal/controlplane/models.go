package controlplane

type Account struct {
	ID           string `json:"id"`
	Email        string `json:"email"`
	Port         int    `json:"port"`
	ProxyMode    string `json:"proxy_mode"`
	CreatedAt    int64  `json:"created_at"`
	GroupEnabled bool   `json:"group_enabled"`
	DefaultGroup bool   `json:"default_group"`
}

type KeyRecord struct {
	Label        string `json:"label"`
	Account      string `json:"account"`
	AccountEmail string `json:"account_email"`
	User         string `json:"user"`
	Status       string `json:"status"`
	Key          string `json:"key"`
	CreatedAt    int64  `json:"created_at"`
	UpdatedAt    int64  `json:"updated_at"`
}

type InternalKey struct {
	Key       string `json:"key"`
	CreatedAt int64  `json:"created_at"`
	Status    string `json:"status"`
}

type SecretStatus struct {
	SHA256    string `json:"sha256"`
	UpdatedAt int64  `json:"updated_at"`
}

type BrandingAsset struct {
	Name        string `db:"name" json:"name"`
	Filename    string `db:"filename" json:"filename"`
	ContentType string `db:"content_type" json:"content_type"`
	Content     []byte `db:"content" json:"content"`
	SHA256      string `db:"sha256" json:"sha256"`
	UpdatedAt   int64  `db:"updated_at" json:"updated_at"`
}

type Team struct {
	ID          string `db:"id" json:"id"`
	Name        string `db:"name" json:"name"`
	Description string `db:"description" json:"description"`
	UserCount   int    `db:"user_count" json:"user_count"`
	CreatedAt   int64  `db:"created_at" json:"created_at"`
	UpdatedAt   int64  `db:"updated_at" json:"updated_at"`
}

type DeletedTeam struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Deleted bool   `json:"deleted"`
}

type TeamAssignment struct {
	User              string  `json:"user"`
	TeamID            *string `json:"team_id"`
	MembershipVersion int64   `json:"membership_version"`
	Changed           bool    `json:"changed"`
	UpdatedAt         int64   `json:"updated_at"`
}

type TeamSummary struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

type UserTeamClassification struct {
	TeamID                *string      `json:"team_id"`
	Team                  *TeamSummary `json:"team"`
	TeamMembershipVersion int64        `json:"team_membership_version"`
}

type TeamExpectation struct {
	Provided bool
	TeamID   *string
}

type UserListOptions struct {
	Query    string
	TeamID   string
	Page     int
	PageSize int
}

type UserSummary struct {
	Email                 string       `db:"email" json:"email"`
	Status                string       `db:"status" json:"status"`
	ActiveKeys            int          `db:"active_keys" json:"active_keys"`
	ActiveAccounts        int          `db:"active_accounts" json:"active_accounts"`
	TotalRecords          int          `db:"total_records" json:"total_records"`
	CreatedAt             int64        `db:"created_at" json:"created_at"`
	UpdatedAt             int64        `db:"updated_at" json:"updated_at"`
	RouteAccountID        *string      `db:"route_account_id" json:"route_account_id"`
	TeamID                *string      `db:"team_id" json:"team_id"`
	Team                  *TeamSummary `json:"team"`
	TeamMembershipVersion int64        `db:"team_membership_version" json:"team_membership_version"`
	TeamName              *string      `db:"team_name" json:"-"`
	TeamDescription       *string      `db:"team_description" json:"-"`
}

type UserPage struct {
	Users      []UserSummary `json:"users"`
	Page       int           `json:"page"`
	PageSize   int           `json:"page_size"`
	Total      int           `json:"total"`
	TotalPages int           `json:"total_pages"`
}
