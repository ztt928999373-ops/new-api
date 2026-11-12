package model

// AggregateCostHead is publication metadata, not a spend ledger.
type AggregateCostHead struct {
	Scope    string `gorm:"primaryKey;size:64"`
	Revision int64
	Digest   string `gorm:"size:64"`
}
type AggregateCostVersion struct {
	ID        string `gorm:"primaryKey;size:64"`
	Scope     string `gorm:"size:64;uniqueIndex:idx_aggregate_cost_revision,priority:1"`
	Revision  int64  `gorm:"uniqueIndex:idx_aggregate_cost_revision,priority:2"`
	Digest    string `gorm:"size:64"`
	Payload   string `gorm:"type:text"`
	State     string `gorm:"size:32"`
	CreatedAt int64
}
