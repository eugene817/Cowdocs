package container

// ContainerConfig struct with Image, Cmd and Tty fields
type ContainerConfig struct {
  Image string
  Cmd []string
  Tty bool
}
