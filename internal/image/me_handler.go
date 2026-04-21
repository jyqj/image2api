package image

import (
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/432539/gpt2api/pkg/resp"
)

// MeHandler 面向本地控制台的图片任务只读接口。
type MeHandler struct{ dao *DAO }

func NewMeHandler(dao *DAO) *MeHandler { return &MeHandler{dao: dao} }

type taskView struct {
	ID             uint64     `json:"id"`
	TaskID         string     `json:"task_id"`
	ModelID        uint64     `json:"model_id"`
	AccountID      uint64     `json:"account_id"`
	Prompt         string     `json:"prompt"`
	N              int        `json:"n"`
	Size           string     `json:"size"`
	Status         string     `json:"status"`
	ConversationID string     `json:"conversation_id,omitempty"`
	Error          string     `json:"error,omitempty"`
	ImageURLs      []string   `json:"image_urls"`
	FileIDs        []string   `json:"file_ids,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
	StartedAt      *time.Time `json:"started_at,omitempty"`
	FinishedAt     *time.Time `json:"finished_at,omitempty"`
}

func toView(t *Task) taskView {
	urls := t.DecodeResultURLs()
	fids := t.DecodeFileIDs()
	for i, id := range fids {
		fids[i] = strings.TrimPrefix(id, "sed:")
	}
	return taskView{
		ID: t.ID, TaskID: t.TaskID, ModelID: t.ModelID,
		AccountID: t.AccountID, Prompt: t.Prompt, N: t.N, Size: t.Size,
		Status: t.Status, ConversationID: t.ConversationID, Error: t.Error,
		ImageURLs: urls, FileIDs: fids, CreatedAt: t.CreatedAt,
		StartedAt: t.StartedAt, FinishedAt: t.FinishedAt,
	}
}

// List GET /api/me/images/tasks。
func (h *MeHandler) List(c *gin.Context) {
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "20"))
	offset, _ := strconv.Atoi(c.DefaultQuery("offset", "0"))
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	if offset < 0 {
		offset = 0
	}
	tasks, err := h.dao.ListAll(c.Request.Context(), limit, offset)
	if err != nil {
		resp.Internal(c, err.Error())
		return
	}
	items := make([]taskView, 0, len(tasks))
	for i := range tasks {
		items = append(items, toView(&tasks[i]))
	}
	resp.OK(c, gin.H{"items": items, "limit": limit, "offset": offset})
}

// Get GET /api/me/images/tasks/:id。
func (h *MeHandler) Get(c *gin.Context) {
	id := c.Param("id")
	if id == "" {
		resp.Fail(c, 40000, "task id required")
		return
	}
	t, err := h.dao.Get(c.Request.Context(), id)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			resp.Fail(c, 40400, "task not found")
			return
		}
		resp.Internal(c, err.Error())
		return
	}
	resp.OK(c, toView(t))
}
