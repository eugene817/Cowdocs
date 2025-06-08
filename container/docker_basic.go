package container

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/events"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"
)

// DockerManager struct with cli field
type DockerManager struct {
	cli *client.Client
}

// StatsRecord holds the final stats for one container run.
type StatsRecord struct {
	ID            string        `json:"id"`
	StartTime     time.Time     `json:"start_time"`
	EndTime       time.Time     `json:"end_time"`
	Duration      time.Duration `json:"duration"`
	CPUUsageNs    uint64        `json:"cpu_usage_ns"`
	MemUsageBytes uint64        `json:"mem_usage_bytes"`
	MemMaxBytes   uint64        `json:"mem_max_bytes"`
}

type ContainerStatsSummary struct {
	Duration    time.Duration `json:"-"`        // raw в наносекундах (не выводим)
	DurationStr string        `json:"Duration"` // человекочитаемый

	// CPU мы теперь храним в nanoseconds (чтобы никогда не было нуля),
	// а уже в JSON конвертируем его в проценты вручную
	CPUUsageNs uint64  `json:"CPUUsageNs"`
	CPUPercent float64 `json:"CPUPercent"`

	// Память храним в байтах, а в JSON делим на 1024, чтобы вывести в kilobytes
	MemUsage   uint64  `json:"-"`
	MemLimit   uint64  `json:"-"`
	MemUsageKB uint64  `json:"MemUsageKB"`
	MemLimitKB uint64  `json:"MemLimitKB"`
	MemPercent float64 `json:"MemPercent"`
}

// cgroupInfo stores the cgroup‐related data we need for a running container.
// We fill this on the "start" event.
type cgroupInfo struct {
	pid      int               // host PID of the container’s init process
	relPaths map[string]string // controller → relative cgroup path (e.g. "/docker/<outer>/docker/<id>")
}

// Docker manager constructor
func NewDockerManager() (*DockerManager, error) {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, err
	}
	return &DockerManager{cli: cli}, nil
}

// ensureImage checks if there is an image of the container
// if not it pulls it.
func (dm *DockerManager) EnsureImage(imageName string) error {
	ctx := context.Background()

	// Inspect the image to check if it exists
	if _, _, err := dm.cli.ImageInspectWithRaw(ctx, imageName); err == nil {
		return nil // Image exists, no need to pull
	}

	// pull
	reader, err := dm.cli.ImagePull(ctx, imageName, image.PullOptions{})
	if err != nil {
		return fmt.Errorf("failed to pull image %s: %w", imageName, err)
	}
	defer reader.Close()
	// close the stream
	if _, err := io.Copy(os.Stdout, reader); err != nil {
		return fmt.Errorf("failed to read pull response for %s: %w", imageName, err)
	}
	return nil
}

// Function to create a container
func (dm *DockerManager) Create(config ContainerConfig) (string, error) {

	// Ensure the image is available
	if err := dm.EnsureImage(config.Image); err != nil {
		return "", err
	}

	ctx := context.Background()
	containerConfig := &container.Config{
		Image: config.Image,
		Cmd:   config.Cmd,
		Tty:   config.Tty,
	}
	resp, err := dm.cli.ContainerCreate(ctx, containerConfig, nil, nil, nil, "")
	if err != nil {
		return "", err
	}
	return resp.ID, nil
}

// Function to start a container
func (dm *DockerManager) Start(containerID string) error {
	ctx := context.Background()
	if err := dm.cli.ContainerStart(ctx, containerID, container.StartOptions{}); err != nil {
		return fmt.Errorf("failed to start container: %v", err)
	}
	return nil
}

// Function to stop a container
func (dm *DockerManager) Stop(containerID string, timeout int) error {
	ctx := context.Background()
	if err := dm.cli.ContainerStop(ctx, containerID, container.StopOptions{Timeout: &[]int{timeout}[0]}); err != nil {
		return fmt.Errorf("failed to stop container: %v", err)
	}
	return nil
}

// Function to remove a container
func (dm *DockerManager) Remove(containerID string) error {
	ctx := context.Background()
	if err := dm.cli.ContainerRemove(ctx, containerID, container.RemoveOptions{Force: true}); err != nil {
		return fmt.Errorf("failed to remove container: %v", err)
	}
	return nil
}

// Function to wait for a container
func (dm *DockerManager) Wait(containerID string) (container.WaitResponse, error) {
	ctx := context.Background()
	respC, errC := dm.cli.ContainerWait(ctx, containerID, container.WaitConditionNotRunning)

	select {
	case resp := <-respC:
		return resp, nil
	case err := <-errC:
		return container.WaitResponse{}, fmt.Errorf("failed to wait for container: %w", err)
	}
}

// Function to check if a container is running
func (dm *DockerManager) IsRunning(containerID string) (bool, error) {
	ctx := context.Background()
	inspect, err := dm.cli.ContainerInspect(ctx, containerID)
	if err != nil {
		return false, fmt.Errorf("failed to inspect container: %v", err)
	}
	return inspect.State.Running, nil
}

// Function to get logs from a container
func (dm *DockerManager) GetLogs(containerID string) (string, error) {
	ctx := context.Background()
	logsReader, err := dm.cli.ContainerLogs(ctx, containerID, container.LogsOptions{ShowStdout: true, ShowStderr: true})
	if err != nil {
		return "", fmt.Errorf("failed to get logs: %v", err)
	}
	defer logsReader.Close()

	var stdout, stderr bytes.Buffer
	if _, err := stdcopy.StdCopy(&stdout, &stderr, logsReader); err != nil {
		return "", fmt.Errorf("failed to read logs: %v", err)
	}

	if stderr.Len() > 0 {
		return "", fmt.Errorf("error in logs: %s", stderr.String())
	}

	return strings.TrimSpace(stdout.String()), nil
}

// Function to get stats from docker container and format them
func (dm *DockerManager) GetStats(containerID string) (string, error) {
	ctx := context.Background()
	stats, err := dm.cli.ContainerStatsOneShot(ctx, containerID)
	if err != nil {
		return "", fmt.Errorf("failed to get stats: %v", err)
	}

	statsBytes, err := io.ReadAll(stats.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read stats: %v", err)
	}
	defer stats.Body.Close()

	var statsData container.StatsResponse
	if err := json.Unmarshal(statsBytes, &statsData); err != nil {
		return "", fmt.Errorf("failed to parse stats: %v", err)
	}

	statsFormatted, err := json.MarshalIndent(statsData, "", " ")
	if err != nil {
		return "", fmt.Errorf("failed to format stats: %v", err)
	}

	return string(statsFormatted), nil
}

func (dm *DockerManager) Ping() error {
	ctx := context.Background()
	_, err := dm.cli.Ping(ctx)
	return err
}

// Monitor: теперь первым делом на "die" печатаем логи, а уже потом — ресурсы.
func Monitor(ctx context.Context, onRecord func(rec StatsRecord)) error {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return fmt.Errorf("could not create Docker client: %w", err)
	}
	defer cli.Close()

	infos := make(map[string]cgroupInfo)

	filterArgs := filters.NewArgs()
	filterArgs.Add("type", "container")
	filterArgs.Add("event", "start")
	filterArgs.Add("event", "die")

	messages, errs := cli.Events(ctx, events.ListOptions{Filters: filterArgs})
	for {
		select {
		case err := <-errs:
			return fmt.Errorf("error from Docker events: %w", err)

		case msg := <-messages:
			switch msg.Action {
			case "start":
				// Собираем PID и relPaths сразу при старте
				pid, relPaths, fetchErr := fetchCgroupInfo(ctx, cli, msg.ID)
				if fetchErr != nil {
					log.Printf("fetchCgroupInfo(%s) error: %v\n", msg.ID, fetchErr)
					// Но даже если не удалось, просто не сохраняем в infos
					continue
				}
				relPaths["__start_time"] = time.Unix(msg.Time, 0).Format(time.RFC3339Nano)
				infos[msg.ID] = cgroupInfo{pid: pid, relPaths: relPaths}

			case "die":
				// Сначала печатаем логи контейнера, даже если cgroupInfo нет или он невалиден
				printContainerLogs(ctx, cli, msg.ID)

				// Затем пытаемся снять метрики, если у нас есть cgroupInfo
				info, ok := infos[msg.ID]
				if !ok {
					log.Printf("no cgroupInfo for %s, skip stats\n", msg.ID)
					break
				}
				// Парсим время старта
				stStr := info.relPaths["__start_time"]
				startTime, err := time.Parse(time.RFC3339Nano, stStr)
				if err != nil {
					startTime = time.Unix(msg.Time, 0)
				}
				endTime := time.Unix(msg.Time, 0)

				rec, err := readStatsFromCgroup(info, msg.ID, startTime, endTime)
				if err != nil {
					log.Printf("readStatsFromCgroup(%s) error: %v\n", msg.ID, err)
					delete(infos, msg.ID)
					break
				}
				onRecord(*rec)
				delete(infos, msg.ID)
			}
		case <-ctx.Done():
			return nil
		}
	}
}

// fetchCgroupInfo inspects a container to find its host PID, then reads /proc/<pid>/cgroup
// to build a map: controller → relative cgroup path. This is called on the "start" event.
//
// Returns (pid, relPaths, error).
func fetchCgroupInfo(ctx context.Context, cli *client.Client, containerID string) (int, map[string]string, error) {
	insp, err := cli.ContainerInspect(ctx, containerID)
	if err != nil {
		return 0, nil, fmt.Errorf("ContainerInspect(%s) failed: %w", containerID, err)
	}
	pid := insp.State.Pid
	if pid <= 0 {
		return 0, nil, fmt.Errorf("invalid PID %d for container %s", pid, containerID)
	}

	// Read /proc/<pid>/cgroup
	cgFile := fmt.Sprintf("/proc/%d/cgroup", pid)
	f, err := os.Open(cgFile)
	if err != nil {
		return pid, nil, fmt.Errorf("could not open %s: %w", cgFile, err)
	}
	defer f.Close()

	relPaths := make(map[string]string)
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		parts := strings.SplitN(line, ":", 3)
		if len(parts) != 3 {
			continue
		}
		controllers := strings.Split(parts[1], ",")
		path := parts[2]
		for _, ctrl := range controllers {
			relPaths[ctrl] = path
		}
	}
	if err := scanner.Err(); err != nil {
		return pid, relPaths, fmt.Errorf("scanner error on %s: %w", cgFile, err)
	}

	return pid, relPaths, nil
}

func readStatsFromCgroup(
	info cgroupInfo,
	containerID string,
	start, end time.Time,
) (*StatsRecord, error) {
	relPaths := info.relPaths
	buildV1 := func(controller, filename string) (string, error) {
		p, ok := relPaths[controller]
		if !ok {
			return "", fmt.Errorf("controller %s not found for %s", controller, containerID)
		}
		return filepath.Join("/sys/fs/cgroup", controller, p, filename), nil
	}

	var cpuUsageNs, memUsageBytes, memMaxBytes uint64
	if unifiedPath, isV2 := relPaths[""]; isV2 {
		cpuStatPath := filepath.Join("/sys/fs/cgroup", unifiedPath, "cpu.stat")
		data, err := ioutil.ReadFile(cpuStatPath)
		if err == nil {
			for _, line := range strings.Split(string(data), "\n") {
				if strings.HasPrefix(line, "usage_usec") {
					var usec uint64
					fmt.Sscanf(line, "usage_usec %d", &usec)
					cpuUsageNs = usec * 1000
					break
				}
			}
		}
		memCur := filepath.Join("/sys/fs/cgroup", unifiedPath, "memory.current")
		if d1, err := ioutil.ReadFile(memCur); err == nil {
			fmt.Sscanf(string(d1), "%d", &memUsageBytes)
		}
		memPeak := filepath.Join("/sys/fs/cgroup", unifiedPath, "memory.peak")
		if d2, err := ioutil.ReadFile(memPeak); err == nil {
			fmt.Sscanf(string(d2), "%d", &memMaxBytes)
		}
	} else {
		if cpuPath, err := buildV1("cpu,cpuacct", "cpuacct.usage"); err == nil {
			if d0, err := ioutil.ReadFile(cpuPath); err == nil {
				fmt.Sscanf(string(d0), "%d", &cpuUsageNs)
			}
		}
		if muPath, err := buildV1("memory", "memory.usage_in_bytes"); err == nil {
			if d1, err := ioutil.ReadFile(muPath); err == nil {
				fmt.Sscanf(string(d1), "%d", &memUsageBytes)
			}
		}
		if mpPath, err := buildV1("memory", "memory.max_usage_in_bytes"); err == nil {
			if d2, err := ioutil.ReadFile(mpPath); err == nil {
				fmt.Sscanf(string(d2), "%d", &memMaxBytes)
			}
		}
	}

	return &StatsRecord{
		ID:            containerID,
		StartTime:     start,
		EndTime:       end,
		Duration:      end.Sub(start),
		CPUUsageNs:    cpuUsageNs,
		MemUsageBytes: memUsageBytes,
		MemMaxBytes:   memMaxBytes,
	}, nil
}

// printContainerLogs читает stdout+stderr контейнера и сразу пишет в консоль.
func printContainerLogs(ctx context.Context, cli *client.Client, containerID string) {
	reader, err := cli.ContainerLogs(ctx, containerID, container.LogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		Follow:     false, // сразу получить весь лог и закрыть
	})
	if err != nil {
		log.Printf("ContainerLogs(%s) error: %v\n", containerID, err)
		return
	}
	defer reader.Close()

	var stdoutBuf, stderrBuf bytes.Buffer
	if _, err := stdcopy.StdCopy(&stdoutBuf, &stderrBuf, reader); err != nil {
		log.Printf("StdCopy error for %s: %v\n", containerID, err)
		return
	}

	outStr := strings.TrimRight(stdoutBuf.String(), "\n")
	errStr := strings.TrimRight(stderrBuf.String(), "\n")
	if outStr != "" {
		log.Printf("Logs of %s (stdout):\n%s\n", containerID, outStr)
	}
	if errStr != "" {
		log.Printf("Logs of %s (stderr):\n%s\n", containerID, errStr)
	}
}

func (dm *DockerManager) GetStatsOneShot(containerID string, startTime time.Time) (ContainerStatsSummary, error) {
	ctx := context.Background()

	statsBody, err := dm.cli.ContainerStatsOneShot(ctx, containerID)
	if err != nil {
		return ContainerStatsSummary{}, fmt.Errorf("ContainerStatsOneShot failed: %w", err)
	}
	defer statsBody.Body.Close()

	raw, err := io.ReadAll(statsBody.Body)
	if err != nil {
		return ContainerStatsSummary{}, fmt.Errorf("read stats body failed: %w", err)
	}

	// Распарсим в стандартную StatsResponse
	var sr container.StatsResponse
	if err := json.Unmarshal(raw, &sr); err != nil {
		return ContainerStatsSummary{}, fmt.Errorf("unmarshal stats response failed: %w", err)
	}

	// 1) Берём «сырое» CPU в наносекундах
	cpuUsageNs := sr.CPUStats.CPUUsage.TotalUsage

	// 2) Дополнительно считаем CPU% (как раньше)
	cpuPercent := 0.0
	cpuDelta := float64(sr.CPUStats.CPUUsage.TotalUsage) -
		float64(sr.PreCPUStats.CPUUsage.TotalUsage)
	systemDelta := float64(sr.CPUStats.SystemUsage) -
		float64(sr.PreCPUStats.SystemUsage)
	if systemDelta > 0 && cpuDelta > 0 {
		cpuPercent = (cpuDelta / systemDelta) *
			float64(len(sr.CPUStats.CPUUsage.PercpuUsage)) * 100.0
	}

	// 3) Память в байтах
	memUsage := sr.MemoryStats.Usage
	memLimit := sr.MemoryStats.Limit

	// 4) Пересчитаем в KB
	memUsageKB := memUsage / 1024
	memLimitKB := memLimit / 1024

	memPercent := 0.0
	if memLimit > 0 {
		memPercent = (float64(memUsage) / float64(memLimit)) * 100.0
	}

	dur := time.Since(startTime)
	return ContainerStatsSummary{
		Duration:    dur,
		DurationStr: dur.String(),
		CPUUsageNs:  cpuUsageNs,
		CPUPercent:  cpuPercent,
		MemUsage:    memUsage,
		MemLimit:    memLimit,
		MemUsageKB:  memUsageKB,
		MemLimitKB:  memLimitKB,
		MemPercent:  memPercent,
	}, nil
}

func (dm *DockerManager) StreamStats(containerID string) (io.ReadCloser, error) {
	// Follow=false → the stream ends as soon as the container stops
	stats, err := dm.cli.ContainerStats(context.Background(), containerID, false)
	if err != nil {
		return nil, err
	}
	return stats.Body, nil
}
