package models

import "time"

// User represents a QuickDesk end-user account.
type User struct {
	ID          uint      `gorm:"primaryKey" json:"id"`
	Username    string    `gorm:"size:64;not null;uniqueIndex" json:"username"`
	Phone       string    `gorm:"size:32" json:"phone"`
	Email       string    `gorm:"size:128" json:"email"`
	Password    string    `gorm:"size:128" json:"-"` // bcrypt hash, never exposed in JSON
	Level       string    `gorm:"size:10;default:'V1'" json:"level"` // V1/V2/V3/V4/V5
	DeviceCount int       `gorm:"default:0" json:"deviceCount"`
	ChannelType string    `gorm:"size:20;default:'全球'" json:"channelType"` // 全球/中国大陆
	Status      bool      `gorm:"default:true" json:"status"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

// TableName overrides the default table name.
func (User) TableName() string {
	return "users"
}
