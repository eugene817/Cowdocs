package container

type Manager interface {
  Create(config ContainerConfig) (string, error)
  Start(id string) error
  Stop(id string) error
  Remove(id string) error
  GetLogs(id string) (string, error)
}
