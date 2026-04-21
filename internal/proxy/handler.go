package proxy

import (
	"context"
	"strconv"
	"sync"

	"github.com/gin-gonic/gin"

	"github.com/432539/gpt2api/pkg/resp"
)

type Handler struct {
	svc    *Service
	prober *Prober
}

func NewHandler(s *Service) *Handler { return &Handler{svc: s} }

// SetProber 在 Prober 初始化完成后注入(避免循环依赖)。未设置时探测接口返回 503。
func (h *Handler) SetProber(p *Prober) { h.prober = p }

// POST /api/admin/proxies
func (h *Handler) Create(c *gin.Context) {
	var req CreateInput
	if err := c.ShouldBindJSON(&req); err != nil {
		resp.BadRequest(c, err.Error())
		return
	}
	p, err := h.svc.Create(c.Request.Context(), req)
	if err != nil {
		resp.Internal(c, err.Error())
		return
	}
	resp.OK(c, p)
}

// GET /api/admin/proxies
func (h *Handler) List(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	if page < 1 {
		page = 1
	}
	size, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))
	if size < 1 || size > 100 {
		size = 20
	}
	list, total, err := h.svc.List(c.Request.Context(), (page-1)*size, size)
	if err != nil {
		resp.Internal(c, err.Error())
		return
	}
	resp.OK(c, gin.H{"list": list, "total": total, "page": page, "page_size": size})
}

// GET /api/admin/proxies/:id
func (h *Handler) Get(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	p, err := h.svc.Get(c.Request.Context(), id)
	if err != nil {
		resp.NotFound(c, err.Error())
		return
	}
	resp.OK(c, p)
}

// PATCH /api/admin/proxies/:id
func (h *Handler) Update(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	var req UpdateInput
	if err := c.ShouldBindJSON(&req); err != nil {
		resp.BadRequest(c, err.Error())
		return
	}
	p, err := h.svc.Update(c.Request.Context(), id, req)
	if err != nil {
		resp.Internal(c, err.Error())
		return
	}
	resp.OK(c, p)
}

// POST /api/admin/proxies/import
// Body: { text, enabled, country, isp, remark, overwrite, probe_after_import }
// 支持每行一个 proxy URL,详见 service.parseProxyLine 的注释。
func (h *Handler) Import(c *gin.Context) {
	var req struct {
		Text             string `json:"text"`
		Enabled          *bool  `json:"enabled,omitempty"`
		Country          string `json:"country"`
		ISP              string `json:"isp"`
		Remark           string `json:"remark"`
		Overwrite        bool   `json:"overwrite"`
		ProbeAfterImport *bool  `json:"probe_after_import,omitempty"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		resp.BadRequest(c, err.Error())
		return
	}
	if len(req.Text) == 0 {
		resp.BadRequest(c, "请至少粘贴一行代理 URL")
		return
	}
	if len(req.Text) > 256*1024 {
		resp.BadRequest(c, "导入内容过大(最多 256KB)")
		return
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	results, err := h.svc.ImportBatch(c.Request.Context(), req.Text, ImportDefaults{
		Enabled: enabled, Country: req.Country, ISP: req.ISP,
		Remark: req.Remark, Overwrite: req.Overwrite,
	})
	if err != nil {
		resp.Internal(c, err.Error())
		return
	}
	// 统计
	var created, updated, skipped, invalid int
	probeIDs := make([]uint64, 0)
	seenProbe := map[uint64]struct{}{}
	for _, r := range results {
		switch r.Status {
		case "created":
			created++
		case "updated":
			updated++
		case "skipped":
			skipped++
		case "invalid":
			invalid++
		}
		if (r.Status == "created" || r.Status == "updated") && r.ID > 0 {
			if _, ok := seenProbe[r.ID]; !ok {
				seenProbe[r.ID] = struct{}{}
				probeIDs = append(probeIDs, r.ID)
			}
		}
	}
	body := gin.H{
		"items":   results,
		"created": created,
		"updated": updated,
		"skipped": skipped,
		"invalid": invalid,
		"total":   len(results),
	}
	probeAfter := true
	if req.ProbeAfterImport != nil {
		probeAfter = *req.ProbeAfterImport
	}
	if probeAfter && h.prober != nil && len(probeIDs) > 0 {
		if len(probeIDs) <= 50 {
			items := h.probeImportedIDs(c.Request.Context(), probeIDs)
			ok, bad := 0, 0
			for _, item := range items {
				if item.OK {
					ok++
				} else {
					bad++
				}
			}
			body["probe"] = gin.H{"total": len(items), "ok": ok, "bad": bad, "items": items}
		} else {
			h.prober.Kick(0)
			body["probe_queued"] = true
		}
	}
	resp.OK(c, body)
}

func (h *Handler) probeImportedIDs(ctx context.Context, ids []uint64) []ProbeResult {
	const conc = 8
	out := make([]ProbeResult, len(ids))
	sem := make(chan struct{}, conc)
	var wg sync.WaitGroup
	for i, id := range ids {
		i, id := i, id
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			res, err := h.prober.ProbeByID(ctx, id)
			if err != nil {
				res = ProbeResult{ProxyID: id, Error: err.Error()}
			}
			out[i] = res
		}()
	}
	wg.Wait()
	return out
}

// POST /api/admin/proxies/:id/probe
// 同步探测单条代理,返回 { ok, latency_ms, error, tried_at, health_score }。
func (h *Handler) Probe(c *gin.Context) {
	if h.prober == nil {
		resp.BadRequest(c, "代理探测器未初始化")
		return
	}
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	res, err := h.prober.ProbeByID(c.Request.Context(), id)
	if err != nil {
		resp.NotFound(c, err.Error())
		return
	}
	// 读回最新的 proxy 以返回更新后的 health_score
	p, _ := h.svc.Get(c.Request.Context(), id)
	resp.OK(c, gin.H{
		"ok":         res.OK,
		"latency_ms": res.LatencyMs,
		"error":      res.Error,
		"tried_at":   res.TriedAt,
		"health_score": func() int {
			if p != nil {
				return p.HealthScore
			}
			return 0
		}(),
	})
}

// POST /api/admin/proxies/probe-all
// 同步探测所有启用的代理,返回完整结果数组(含统计)。
func (h *Handler) ProbeAll(c *gin.Context) {
	if h.prober == nil {
		resp.BadRequest(c, "代理探测器未初始化")
		return
	}
	results, err := h.prober.ProbeAll(c.Request.Context())
	if err != nil {
		resp.Internal(c, err.Error())
		return
	}
	ok, bad := 0, 0
	for _, r := range results {
		if r.OK {
			ok++
		} else {
			bad++
		}
	}
	resp.OK(c, gin.H{
		"total": len(results),
		"ok":    ok,
		"bad":   bad,
		"items": results,
	})
}

// DELETE /api/admin/proxies/:id
func (h *Handler) Delete(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	if err := h.svc.Delete(c.Request.Context(), id); err != nil {
		resp.Internal(c, err.Error())
		return
	}
	resp.OK(c, gin.H{"deleted": id})
}
