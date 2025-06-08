package api

import (
	"encoding/json"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/eugene817/Cowdocs/container"
)

// API struct with containerManager field
type API struct {
	containerManager container.Manager
}

// Function to create a new API instance
func NewAPI(mgr container.Manager) *API {
	return &API{containerManager: mgr}
}

// StatsResponse — минимальный набор полей из Docker‐статистики, которые нам нужны.
type StatsResponse struct {
	CPUStats struct {
		// TotalUsage — кумулятивные наносекунды CPU
		CPUUsage struct {
			TotalUsage uint64 `json:"total_usage"`
		} `json:"cpu_usage"`
		// SystemUsage — кумулятивные наносекунды всех ядер
		SystemUsage uint64 `json:"system_cpu_usage"`
	} `json:"cpu_stats"`

	// PreCPUStats нужен, если ты захочешь рассчитывать %-ные изменения по docker-алгоритму
	PreCPUStats struct {
		CPUUsage struct {
			TotalUsage uint64 `json:"total_usage"`
		} `json:"cpu_usage"`
		SystemUsage uint64 `json:"system_cpu_usage"`
	} `json:"precpu_stats"`

	MemoryStats struct {
		Usage uint64 `json:"usage"`
		Limit uint64 `json:"limit"`
	} `json:"memory_stats"`
}

// RunContainer создаёт, запускает контейнер, стримит stats, ждёт финиша и возвращает логи + JSON-метрики.
func (api *API) RunContainer(cfg container.ContainerConfig, showStats bool) (string, string, error) {
	// 1) Create + defer Remove
	id, err := api.containerManager.Create(cfg)
	if err != nil {
		return "", "", fmt.Errorf("create: %w", err)
	}
	defer api.containerManager.Remove(id)

	// 2) Start + засечь момент старта
	start := time.Now()
	if err := api.containerManager.Start(id); err != nil {
		return "", "", fmt.Errorf("start: %w", err)
	}

	// 3) Если нужна статистика — запустить стример
	var (
		last      container.StatsResponse
		streamErr error
		wg        sync.WaitGroup
	)
	if showStats {
		statsRdr, err := api.containerManager.StreamStats(id)
		if err != nil {
			return "", "", fmt.Errorf("stream stats: %w", err)
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer statsRdr.Close()
			dec := json.NewDecoder(statsRdr)
			for {
				var sr container.StatsResponse
				if err := dec.Decode(&sr); err != nil {
					if err == io.EOF {
						return
					}
					streamErr = err
					return
				}
				last = sr
			}
		}()
	}

	// 4) Ждём, пока контейнер завершится
	if _, err := api.containerManager.Wait(id); err != nil {
		return "", "", fmt.Errorf("wait: %w", err)
	}
	// Дождаться статистического потока
	if showStats {
		wg.Wait()
		if streamErr != nil {
			return "", "", fmt.Errorf("stats stream: %w", streamErr)
		}
	}

	// 5) Собрать логи
	logs, err := api.containerManager.GetLogs(id)
	if err != nil {
		return "", "", fmt.Errorf("logs: %w", err)
	}

	// 6) Пост-обработка и JSON-ответ по stats
	var statsJSON string
	if showStats {
		end := time.Now()
		dur := end.Sub(start)

		// CPU
		cpuNs := last.CPUStats.CPUUsage.TotalUsage - last.PreCPUStats.CPUUsage.TotalUsage
		if last.PreCPUStats.CPUUsage.TotalUsage == 0 {
			cpuNs = last.CPUStats.CPUUsage.TotalUsage
		}
		cpuPercent := float64(cpuNs) / float64(dur.Nanoseconds()) * 100

		// Память
		memUsage := last.MemoryStats.Usage
		memLimit := last.MemoryStats.Limit
		memKB := memUsage / 1024
		limitKB := memLimit / 1024
		memPercent := 0.0
		if memLimit > 0 {
			memPercent = float64(memUsage) / float64(memLimit) * 100
		}

		summary := container.ContainerStatsSummary{
			DurationStr: dur.String(),
			CPUUsageNs:  cpuNs,
			CPUPercent:  cpuPercent,
			MemUsageKB:  memKB,
			MemLimitKB:  limitKB,
			MemPercent:  memPercent,
		}
		b, _ := json.MarshalIndent(summary, "", "  ")
		statsJSON = string(b)
	}

	return logs, statsJSON, nil
}

// Function to run containers in parallel using goroutines and channels.
// The container logs are sent to the channel.
// Creates. Starts, Waits and Removes the container.
func (api *API) RunContainerParallel(config container.ContainerConfig, wg *sync.WaitGroup, c chan string) error {
	defer wg.Done()
	id, err := api.containerManager.Create(config)
	if err != nil {
		return fmt.Errorf("failed to create container: %v", err)
	}
	defer api.containerManager.Remove(id)

	if err := api.containerManager.Start(id); err != nil {
		return fmt.Errorf("failed to start container: %v", err)
	}

	if _, err := api.containerManager.Wait(id); err != nil {
		return fmt.Errorf("failed to wait for container: %v", err)
	}

	logs, err := api.containerManager.GetLogs(id)
	if err != nil {
		return fmt.Errorf("failed to get logs: %v", err)
	}
	c <- logs
	return nil
}

func (api *API) EnsureImages(images []string) error {
	for _, image := range images {
		if err := api.containerManager.EnsureImage(image); err != nil {
			return fmt.Errorf("failed to ensure image %s: %v", image, err)
		}
	}
	return nil
}

func (api *API) Ping() error {
	return api.containerManager.Ping()
}
