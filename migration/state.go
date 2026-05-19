package migration

// VMStatePayload carries serialized VM state next to the manifest fields needed
// to attach it to a VM resource.
type VMStatePayload struct {
	ID        string            `json:"id"`
	Kind      string            `json:"kind,omitempty"`
	StatePath string            `json:"state_path,omitempty"`
	Labels    map[string]string `json:"labels,omitempty"`
	Data      []byte            `json:"data,omitempty"`
}

// TaskStatePayload carries serialized task checkpoint state next to the
// manifest fields needed to attach it to a task.
type TaskStatePayload struct {
	ID        string            `json:"id"`
	Kind      string            `json:"kind,omitempty"`
	StatePath string            `json:"state_path,omitempty"`
	Labels    map[string]string `json:"labels,omitempty"`
	Data      []byte            `json:"data,omitempty"`
}

// VMStatePath returns the default bundle path for a VM state payload.
func VMStatePath(id string) string {
	return ".wanix-vmstate-" + id + ".bin"
}

// TaskStatePath returns the default bundle path for a task state payload.
func TaskStatePath(id string) string {
	return ".wanix-taskstate-" + id + ".bin"
}
