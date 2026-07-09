package provisioner

import (
	"sort"
	"time"
)

type BuilderStatusInfo struct {
	ID       string    `json:"id"`
	IP       string    `json:"ip"`
	Arch     string    `json:"arch"`
	Status   string    `json:"status"`
	DrvPath  string    `json:"drvPath,omitempty"`
	Created  time.Time `json:"created"`
	LastUsed time.Time `json:"lastUsed"`
	AgeSecs  int64     `json:"ageSecs"`
}

type PoolStatusInfo struct {
	Builders          []BuilderStatusInfo `json:"builders"`
	PendingRequests   int                 `json:"pendingRequests"`
	ProvisioningCount map[string]int      `json:"provisioningCount"`
}

func builderStatusString(s BuilderStatus) string {
	switch s {
	case BuilderStatusProvisioning:
		return "provisioning"
	case BuilderStatusReady:
		return "ready"
	case BuilderStatusInUse:
		return "in_use"
	case BuilderStatusPooled:
		return "pooled"
	case BuilderStatusDestroying:
		return "destroying"
	default:
		return "unknown"
	}
}

// Status returns a snapshot of the current pool state (safe to call concurrently).
func (bp *Pool) Status() PoolStatusInfo {
	bp.mu.Lock()
	defer bp.mu.Unlock()

	now := time.Now()
	builders := make([]BuilderStatusInfo, 0, len(bp.builders))
	for _, b := range bp.builders {
		builders = append(builders, BuilderStatusInfo{
			ID:       b.ID,
			IP:       b.IP,
			Arch:     b.Arch,
			Status:   builderStatusString(b.Status),
			DrvPath:  b.DrvPath,
			Created:  b.Created,
			LastUsed: b.LastUsed,
			AgeSecs:  int64(now.Sub(b.Created).Seconds()),
		})
	}
	sort.Slice(builders, func(i, j int) bool {
		return builders[i].Created.After(builders[j].Created)
	})

	provCount := make(map[string]int)
	for arch, count := range bp.provisioningCount {
		if count > 0 {
			provCount[arch] = count
		}
	}

	pending := 0
	for _, req := range bp.waiters {
		if req.Ctx.Err() == nil {
			pending++
		}
	}

	return PoolStatusInfo{
		Builders:          builders,
		PendingRequests:   pending,
		ProvisioningCount: provCount,
	}
}
