package scheduler

// ConnectionInfo exposes only what the scheduler needs, preventing cyclic dependencies.
type ConnectionInfo interface {
	GetConnectionID() [16]byte
	GetSubflowCount() int
	MarkSubflowScheduled(index int)
}

// SchedulerOps perfectly mirrors struct mptcp_sched_ops from Section 7.1
type SchedulerOps interface {
	Name() string
	Init(conn ConnectionInfo)
	Release(conn ConnectionInfo)
	SelectSubflow(conn ConnectionInfo) int
}
