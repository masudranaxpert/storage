package lifecycle

// Store defines the persistence contract for flow policy, per-file state, tasks, and expire audits.
type Store interface {
	GetFlowPolicy() (*FlowPolicy, error)
	SaveFlowPolicy(p *FlowPolicy) error

	GetLifecycleState(key string) (*FileState, error)
	SaveLifecycleState(st *FileState) error
	DeleteLifecycleState(key string) error
	ListLifecycleStates() ([]*FileState, error)

	GetLifecycleTask(id string) (*Task, error)
	SaveLifecycleTask(t *Task) error
	ListLifecycleTasks() ([]*Task, error)

	GetActiveLifecycleTask(key string) (string, error)
	SetActiveLifecycleTask(key, taskID string) error
	DeleteActiveLifecycleTask(key string) error
	ListActiveLifecycleTasks() (map[string]string, error)

	SaveLifecycleAudit(entry *AuditEntry) error
	ListLifecycleAudits(limit int) ([]*AuditEntry, error)
}
