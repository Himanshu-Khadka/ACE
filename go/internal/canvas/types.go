package canvas

import (
	"encoding/json"
	"time"
)

// Only the fields ACE actually uses. Canvas sends a lot more.

type Course struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	CourseCode string `json:"course_code"`
	Term       *struct {
		Name string `json:"name"`
	} `json:"term"`
}

type Module struct {
	ID    int64        `json:"id"`
	Name  string       `json:"name"`
	Items []ModuleItem `json:"items"` // nil when the module has too many items
}

type ModuleItem struct {
	Title     string `json:"title"`
	Type      string `json:"type"` // File, Page, Assignment, ExternalUrl, ...
	ContentID int64  `json:"content_id"`
	PageURL   string `json:"page_url"`
}

type File struct {
	ID            int64      `json:"id"`
	DisplayName   string     `json:"display_name"`
	URL           string     `json:"url"`
	Size          int64      `json:"size"`
	ContentType   string     `json:"content-type"`
	UpdatedAt     *time.Time `json:"updated_at"`
	LockedForUser bool       `json:"locked_for_user"`
}

type Page struct {
	PageID    int64      `json:"page_id"`
	URL       string     `json:"url"`
	Title     string     `json:"title"`
	Body      string     `json:"body"`
	HTMLURL   string     `json:"html_url"`
	UpdatedAt *time.Time `json:"updated_at"`
}

type Assignment struct {
	ID               int64       `json:"id"`
	Name             string      `json:"name"`
	Description      string      `json:"description"`
	DueAt            *time.Time  `json:"due_at"`
	UpdatedAt        *time.Time  `json:"updated_at"`
	HTMLURL          string      `json:"html_url"`
	PointsPossible   *float64    `json:"points_possible"`
	SubmissionTypes  []string    `json:"submission_types"`
	QuizID           int64       `json:"quiz_id"`
	IsQuizAssignment bool        `json:"is_quiz_assignment"`
	Submission       *Submission `json:"submission"`
}

type Submission struct {
	WorkflowState string     `json:"workflow_state"` // unsubmitted, submitted, graded, pending_review
	SubmittedAt   *time.Time `json:"submitted_at"`
	Score         *float64   `json:"score"`
	Missing       bool       `json:"missing"`
	Excused       bool       `json:"excused"`
	Late          bool       `json:"late"`
}

type Event struct {
	ID           int64      `json:"id"`
	Title        string     `json:"title"`
	Description  string     `json:"description"`
	StartAt      *time.Time `json:"start_at"`
	EndAt        *time.Time `json:"end_at"`
	AllDay       bool       `json:"all_day"`
	LocationName string     `json:"location_name"`
	HTMLURL      string     `json:"html_url"`
}

type Announcement struct {
	ID          int64      `json:"id"`
	Title       string     `json:"title"`
	Message     string     `json:"message"`
	PostedAt    *time.Time `json:"posted_at"`
	LastReplyAt *time.Time `json:"last_reply_at"`
	HTMLURL     string     `json:"html_url"`
}

type PlannerItem struct {
	PlannableType string     `json:"plannable_type"`
	PlannableID   int64      `json:"plannable_id"`
	PlannableDate *time.Time `json:"plannable_date"`
	CourseID      int64      `json:"course_id"`
	HTMLURL       string     `json:"html_url"`
	Plannable     struct {
		Title          string   `json:"title"`
		Details        string   `json:"details"`
		PointsPossible *float64 `json:"points_possible"`
		AssignmentID   int64    `json:"assignment_id"`
	} `json:"plannable"`
	// canvas sends `false` here when there's nothing to submit, otherwise an object
	Submissions     json.RawMessage `json:"submissions"`
	PlannerOverride *struct {
		MarkedComplete bool `json:"marked_complete"`
	} `json:"planner_override"`
}

type PlannerSubmissions struct {
	Submitted bool `json:"submitted"`
	Excused   bool `json:"excused"`
	Graded    bool `json:"graded"`
	Missing   bool `json:"missing"`
}

func (p PlannerItem) SubmissionState() PlannerSubmissions {
	var s PlannerSubmissions
	_ = json.Unmarshal(p.Submissions, &s) // `false` just leaves it empty
	return s
}
