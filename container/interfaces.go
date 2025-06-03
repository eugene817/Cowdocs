package container

import (
	"github.com/docker/docker/api/types/container"
	"time"
)

// Manager interface with methods to manage containers
type Manager interface {
	Create(config ContainerConfig) (string, error)
	Start(id string) error
	Stop(id string, timeout int) error
	Remove(id string) error
	GetLogs(id string) (string, error)
	Wait(id string) (container.WaitResponse, error)
	IsRunning(id string) (bool, error)
	GetStats(containerID string) (string, error)
	EnsureImage(image string) error
	GetStatsOneShot(containerID string, startTime time.Time) (ContainerStatsSummary, error)
	Ping() error
}
