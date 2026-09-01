package connection

type SavedQuery struct {
	ID                    string `json:"id"`
	Name                  string `json:"name"`
	SQL                   string `json:"sql"`
	ConnectionID          string `json:"connectionId"`
	DBName                string `json:"dbName"`
	CreatedAt             int64  `json:"createdAt"`
	ConnectionFingerprint string `json:"connectionFingerprint,omitempty"`
	FingerprintVersion    string `json:"fingerprintVersion,omitempty"`
	BindingStatus         string `json:"bindingStatus,omitempty"`
	OriginalConnectionID  string `json:"originalConnectionId,omitempty"`
}

// SavedQueryGroup describes a user-managed saved SQL folder. QueryIDs contains
// only the group's direct queries; ChildOrder can mix query:<id> and group:<id>
// tokens to retain the visible order of direct queries and child groups.
type SavedQueryGroup struct {
	ID            string   `json:"id"`
	Name          string   `json:"name"`
	ParentGroupID string   `json:"parentGroupId"`
	QueryIDs      []string `json:"queryIds"`
	ChildOrder    []string `json:"childOrder"`
}

type SavedQueryImportPayload struct {
	Queries           []SavedQuery           `json:"queries"`
	Groups            []SavedQueryGroup      `json:"groups,omitempty"`
	LegacyConnections []SavedConnectionInput `json:"legacyConnections,omitempty"`
}
