package discovery

import "time"

// Status of a registered instance.
type Status string

const (
	StatusUp       Status = "up"
	StatusDown     Status = "down"
	StatusStarting Status = "starting"
	StatusDraining Status = "draining"
)

// CheckMode chooses how the discovery server determines this instance's
// liveness.
//
// CheckHeartbeat (default when empty) — your service must call Heartbeat
// periodically (the auto-heartbeat goroutine started by Register handles
// this); the server marks the instance down when no heartbeat arrives
// within TTLSeconds.
//
// CheckHTTP — the server actively GETs each HTTP/HTTPS interface
// (HealthURL takes precedence over Path; both empty means "/" with any
// 2xx-3xx counted as up). All HTTP-ish interfaces must pass.
//
// CheckTCP — the server opens a TCP connection to every interface's port.
// All must succeed.
//
// CheckNone — the server never auto-changes status. Use only when an
// external system manages the status field via the REST API.
type CheckMode string

const (
	CheckHeartbeat CheckMode = "heartbeat"
	CheckHTTP      CheckMode = "http"
	CheckTCP       CheckMode = "tcp"
	CheckNone      CheckMode = "none"
)

// Service is a logical service registered in discovery.
type Service struct {
	Name        string            `json:"name"`
	Description string            `json:"description,omitempty"`
	Tags        []string          `json:"tags,omitempty"`
	Metadata    map[string]string `json:"metadata,omitempty"`
	CreatedAt   time.Time         `json:"createdAt"`
	UpdatedAt   time.Time         `json:"updatedAt"`
}

// Interface is one network endpoint exposed by an instance,
// e.g. {Name:"WEB", Protocol:"http", Port:8080}.
type Interface struct {
	Name      string            `json:"name"`
	Protocol  string            `json:"protocol"`
	Port      int               `json:"port"`
	Path      string            `json:"path,omitempty"`
	TLS       bool              `json:"tls,omitempty"`
	HealthURL string            `json:"healthUrl,omitempty"`
	Metadata  map[string]string `json:"metadata,omitempty"`
}

// ProbeResult is the per-interface outcome from a server-side health probe.
// Returned inside InstanceCheck on Discover/ListInstances responses.
type ProbeResult struct {
	Interface  string `json:"interface"`
	URL        string `json:"url,omitempty"`
	OK         bool   `json:"ok"`
	HTTPStatus int    `json:"httpStatus,omitempty"`
	Error      string `json:"error,omitempty"`
	LatencyMS  int64  `json:"latencyMs"`
}

// InstanceCheck is the most recent probe outcome attached to an Instance.
// Only populated when CheckMode is "http" or "tcp"; nil for heartbeat-only
// instances.
type InstanceCheck struct {
	Timestamp time.Time     `json:"timestamp"`
	OK        bool          `json:"ok"`
	Mode      CheckMode     `json:"mode,omitempty"`
	Results   []ProbeResult `json:"results,omitempty"`
}

// Instance is a single running process registered for a Service.
type Instance struct {
	ID          string            `json:"id"`
	ServiceName string            `json:"service"`
	Address     string            `json:"address"`
	Interfaces  []Interface       `json:"interfaces"`
	Weight      int               `json:"weight"`
	Status      Status            `json:"status"`
	Metadata    map[string]string `json:"metadata,omitempty"`
	TTLSeconds  int               `json:"ttlSeconds"`

	// Liveness configuration (set on registration).
	CheckMode        CheckMode `json:"checkMode,omitempty"`
	CheckIntervalSec int       `json:"checkIntervalSec,omitempty"`

	// LastCheck is set by the server when CheckMode is http/tcp; it carries
	// the latest probe result with per-interface details.
	LastCheck *InstanceCheck `json:"lastCheck,omitempty"`

	LastHeartbeat time.Time `json:"lastHeartbeat"`
	RegisteredAt  time.Time `json:"registeredAt"`
	UpdatedAt     time.Time `json:"updatedAt"`
}

// Event is a change notification streamed over WebSocket.
type Event struct {
	Type      string      `json:"type"`
	Service   string      `json:"service,omitempty"`
	Instance  string      `json:"instance,omitempty"`
	Payload   interface{} `json:"payload,omitempty"`
	Timestamp time.Time   `json:"timestamp"`
	OriginID  string      `json:"originId,omitempty"`
}

// Event types broadcast over /v1/watch.
const (
	EventServiceUpserted  = "service.upserted"
	EventServiceDeleted   = "service.deleted"
	EventInstanceUpserted = "instance.upserted"
	EventInstanceDeleted  = "instance.deleted"
	EventInstanceStatus   = "instance.status"
)
