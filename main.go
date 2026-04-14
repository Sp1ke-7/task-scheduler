package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
	"github.com/robfig/cron/v3"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

// Task 是任务调度系统中的任务模型。
// GORM 会根据该结构体自动创建/更新数据库表结构。
type Task struct {
	ID        uint      `gorm:"primaryKey" json:"id"`                    // 主键 ID
	Name      string    `gorm:"type:varchar(255);not null" json:"name"`  // 任务名称
	Status    string    `gorm:"type:varchar(50);not null" json:"status"` // 任务状态
	CreatedAt time.Time `json:"created_at"`                               // 创建时间
	UpdatedAt time.Time `json:"updated_at"`                               // 更新时间
}

// CreateTaskRequest 定义创建任务接口的请求体。
type CreateTaskRequest struct {
	Name string `json:"name"` // 任务名称
}

// getEnvOrDefault 读取环境变量，若不存在则返回默认值。
func getEnvOrDefault(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

// startWorkerPool 在服务启动时拉起固定数量的 Worker。
func startWorkerPool(db *gorm.DB, redisClient *redis.Client, workerCount int) {
	for i := 1; i <= workerCount; i++ {
		go runWorker(db, redisClient, i)
	}
}

// runWorker 持续轮询 pending 任务并执行。
func runWorker(db *gorm.DB, redisClient *redis.Client, workerID int) {
	ctx := context.Background()

	for {
		var pendingTasks []Task

		// 每轮拉取一批 pending 任务，逐个尝试抢 Redis 锁。
		// 这样抢锁失败时可以继续尝试下一个任务，而不是卡在同一个任务上。
		err := db.Where("status = ?", "pending").Order("id ASC").Limit(10).Find(&pendingTasks).Error
		if err != nil {
			log.Printf("worker-%d 查询任务失败: %v", workerID, err)
			time.Sleep(1 * time.Second)
			continue
		}

		if len(pendingTasks) == 0 {
			time.Sleep(1 * time.Second)
			continue
		}

		handledTask := false
		for _, task := range pendingTasks {
			lockKey := fmt.Sprintf("task_lock:%d", task.ID)

			// 先通过 Redis SETNX 抢占分布式锁，只有抢到锁的 Worker 才能继续。
			// 锁 TTL 固定为 10 秒，避免异常退出导致死锁。
			locked, lockErr := redisClient.SetNX(ctx, lockKey, workerID, 10*time.Second).Result()
			if lockErr != nil {
				log.Printf("worker-%d Redis 抢锁失败 (ID=%d): %v", workerID, task.ID, lockErr)
				continue
			}
			if !locked {
				// 抢锁失败表示其他 Worker 正在处理该任务，尝试下一个 pending 任务。
				continue
			}

			// 保留数据库条件更新作为第二层保障：
			// 只有 status 仍然是 pending，才能更新为 processing。
			updateResult := db.Model(&Task{}).
				Where("id = ? AND status = ?", task.ID, "pending").
				Update("status", "processing")
			if updateResult.Error != nil {
				log.Printf("worker-%d 抢占任务失败 (ID=%d): %v", workerID, task.ID, updateResult.Error)
				_ = redisClient.Del(ctx, lockKey).Err()
				continue
			}
			if updateResult.RowsAffected == 0 {
				_ = redisClient.Del(ctx, lockKey).Err()
				continue
			}

			handledTask = true

			// 抢占成功，开始执行任务。
			log.Printf("正在执行任务 [%d]", task.ID)
			time.Sleep(2 * time.Second) // 模拟任务执行耗时

			// 执行完成后将状态置为 completed。
			if err := db.Model(&Task{}).Where("id = ?", task.ID).Update("status", "completed").Error; err != nil {
				log.Printf("worker-%d 完成任务更新失败 (ID=%d): %v", workerID, task.ID, err)
			}

			// 任务处理结束后释放锁。
			if err := redisClient.Del(ctx, lockKey).Err(); err != nil {
				log.Printf("worker-%d 释放任务锁失败 (ID=%d): %v", workerID, task.ID, err)
			}

			// 单个 Worker 每轮只处理一个任务，完成后进入下一轮轮询。
			break
		}

		if !handledTask {
			time.Sleep(1 * time.Second)
		}
	}
}

func main() {
	// 1) 初始化 Gin 引擎（使用默认中间件：logger + recovery）
	router := gin.Default()

	// 2) 连接 MySQL
	// DSN 格式: 用户名:密码@tcp(地址:端口)/数据库?参数
	// 连接信息优先取环境变量，不存在时回退到默认值。
	dbHost := getEnvOrDefault("DB_HOST", "localhost")
	dbPort := getEnvOrDefault("DB_PORT", "3306")
	dbUser := getEnvOrDefault("DB_USER", "root")
	dbPassword := getEnvOrDefault("DB_PASSWORD", "123456")
	dbName := getEnvOrDefault("DB_NAME", "task_db")
	dsn := fmt.Sprintf("%s:%s@tcp(%s:%s)/%s?charset=utf8mb4&parseTime=True&loc=Local", dbUser, dbPassword, dbHost, dbPort, dbName)
	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{})
	if err != nil {
		log.Fatalf("连接 MySQL 失败: %v", err)
	}

	// 3) + 4) 自动迁移 Task 表结构
	if err := db.AutoMigrate(&Task{}); err != nil {
		log.Fatalf("自动迁移失败: %v", err)
	}

	// 5) 初始化 Redis 客户端（用于任务分布式锁）
	redisAddr := getEnvOrDefault("REDIS_ADDR", "localhost:6379")
	redisClient := redis.NewClient(&redis.Options{
		Addr: redisAddr,
	})

	// 启动时先做一次 Ping，尽早暴露连接问题。
	if err := redisClient.Ping(context.Background()).Err(); err != nil {
		log.Fatalf("连接 Redis 失败: %v", err)
	}

	// 启动 3 个 Worker goroutine，后台消费 pending 任务
	startWorkerPool(db, redisClient, 3)

	// 6) 初始化 cron 调度器，并注册每分钟自动创建任务的定时任务。
	// 使用标准 5 段 cron 表达式，含义是“每分钟执行一次”。
	cronExpr := "* * * * *"
	cronScheduler := cron.New()
	cronEntryID, err := cronScheduler.AddFunc(cronExpr, func() {
		// 定时创建的任务保持与手动创建一致：name=cron-task, status=pending。
		// 这样可被现有 Worker 正常消费，无需改动 Worker 逻辑。
		cronTask := Task{
			Name:   "cron-task",
			Status: "pending",
		}

		if err := db.Create(&cronTask).Error; err != nil {
			log.Printf("cron 创建任务失败: %v", err)
			return
		}

		log.Printf("cron 创建任务成功: ID=%d", cronTask.ID)
	})
	if err != nil {
		log.Fatalf("注册 cron 任务失败: %v", err)
	}
	cronScheduler.Start()

	// 7) 提供 GET /ping 接口，返回 {"message": "pong"}
	router.GET("/ping", func(c *gin.Context) {
		c.JSON(200, gin.H{
			"message": "pong",
		})
	})

	// 8) 提供 GET /cron/status，返回 cron 表达式和下次执行时间。
	router.GET("/cron/status", func(c *gin.Context) {
		entry := cronScheduler.Entry(cronEntryID)

		// 如果 next 为零值，说明调度器尚未计算出下一次执行时间。
		nextRun := ""
		if !entry.Next.IsZero() {
			nextRun = entry.Next.Format(time.RFC3339)
		}

		c.JSON(200, gin.H{
			"cron_expression": cronExpr,
			"next_run_time":   nextRun,
		})
	})

	// POST /tasks: 创建任务
	// - 接收 JSON: {"name": "任务名称"}
	// - 默认状态为 pending
	router.POST("/tasks", func(c *gin.Context) {
		var req CreateTaskRequest

		// 解析请求体 JSON
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(400, gin.H{
				"error": "请求体格式错误",
			})
			return
		}

		// 简单参数校验：name 不能为空
		if req.Name == "" {
			c.JSON(400, gin.H{
				"error": "name 不能为空",
			})
			return
		}

		// 构建任务对象并设置默认状态
		task := Task{
			Name:   req.Name,
			Status: "pending",
		}

		// 保存到数据库
		if err := db.Create(&task).Error; err != nil {
			c.JSON(500, gin.H{
				"error": "创建任务失败",
			})
			return
		}

		// 返回 201 和创建成功的任务信息
		c.JSON(201, task)
	})

	// GET /tasks: 查询所有任务（按创建时间倒序）
	router.GET("/tasks", func(c *gin.Context) {
		var tasks []Task
		if err := db.Order("created_at DESC").Find(&tasks).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{
				"error": "查询任务列表失败",
			})
			return
		}

		c.JSON(http.StatusOK, tasks)
	})

	// GET /tasks/:id: 查询单个任务详情
	router.GET("/tasks/:id", func(c *gin.Context) {
		idParam := c.Param("id")
		taskID, err := strconv.ParseUint(idParam, 10, 64)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{
				"error": "无效的任务 ID",
			})
			return
		}

		var task Task
		if err := db.First(&task, taskID).Error; err != nil {
			if err == gorm.ErrRecordNotFound {
				c.JSON(http.StatusNotFound, gin.H{
					"error": "任务不存在",
				})
				return
			}

			c.JSON(http.StatusInternalServerError, gin.H{
				"error": "查询任务详情失败",
			})
			return
		}

		c.JSON(http.StatusOK, task)
	})

	// DELETE /tasks/:id: 删除任务（硬删除）
	router.DELETE("/tasks/:id", func(c *gin.Context) {
		idParam := c.Param("id")
		taskID, err := strconv.ParseUint(idParam, 10, 64)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{
				"error": "无效的任务 ID",
			})
			return
		}

		deleteResult := db.Delete(&Task{}, taskID)
		if deleteResult.Error != nil {
			c.JSON(http.StatusInternalServerError, gin.H{
				"error": "删除任务失败",
			})
			return
		}
		if deleteResult.RowsAffected == 0 {
			c.JSON(http.StatusNotFound, gin.H{
				"error": "任务不存在",
			})
			return
		}

		c.JSON(http.StatusOK, gin.H{
			"message": "任务删除成功",
		})
	})

	// 启动 HTTP 服务
	addr := ":8080"
	fmt.Printf("server running at http://127.0.0.1%s\n", addr)
	if err := router.Run(addr); err != nil {
		log.Fatalf("Gin 服务启动失败: %v", err)
	}
}
