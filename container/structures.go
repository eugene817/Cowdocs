package container

type ContainerConfig struct {
  Image string
  Cmd []string
  Tty bool
}
