package models

import "time"

// TextToVideoProject is a text-to-video project: a list of prompt lines, each
// generating one video clip via a RunningHub-hosted text-to-video workflow.
type TextToVideoProject struct {
	ID          uint      `json:"id" gorm:"primaryKey"`
	Name        string    `json:"name"`
	Code        string    `json:"code" gorm:"uniqueIndex"`
	Description string    `json:"description"`
	Text        string    `json:"text"` // newline/blank-line separated prompts
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// TextToVideoLine is one prompt → one generated video clip.
type TextToVideoLine struct {
	ID                uint      `json:"id" gorm:"primaryKey"`
	ProjectID         uint      `json:"project_id" gorm:"index"`
	SortOrder         int       `json:"sort_order" gorm:"default:1"`
	Prompt            string    `json:"prompt"`
	NegativePrompt    string    `json:"negative_prompt"`
	Seed              int64     `json:"seed" gorm:"default:-1"`
	Status            string    `json:"status" gorm:"default:'draft'"` // draft|generating|generated|failed
	CurrentTaskID     string    `json:"current_task_id"`
	LastError         string    `json:"last_error"`
	GeneratedVideo    string    `json:"generated_video"`
	GeneratedWorkflow string    `json:"generated_workflow"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`
}
