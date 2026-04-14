# Task Scheduler - 分布式任务调度系统

基于 Go 语言开发的轻量级分布式任务调度系统，支持任务 CRUD、并发消费、Redis 分布式锁、Cron 定时任务，以及 Docker Compose 一键部署。

## 技术栈
- 语言：Go 1.25
- 框架：Gin
- 数据库：MySQL (GORM)
- 缓存/锁：Redis (go-redis)
- 并发：Goroutine Worker Pool
- 定时任务：robfig/cron/v3
- 部署：Docker + Docker Compose

## 核心功能
- 任务 CRUD 接口（创建、查询、删除）
- Worker 池并发消费任务（3 个 Worker）
- Redis 分布式锁（SETNX）防止重复执行
- 任务状态流转（pending -> processing -> completed）
- Cron 定时任务（每分钟自动创建任务）
- Docker Compose 一键部署（MySQL + Redis + App）

## 快速启动
docker-compose up -d

## API 文档
| 方法 | 路径 | 功能 |
|:---|:---|:---|
| GET | /ping | 健康检查 |
| POST | /tasks | 创建任务 |
| GET | /tasks | 查询所有任务 |
| GET | /tasks/:id | 查询单个任务 |
| DELETE | /tasks/:id | 删除任务 |
| GET | /cron/status | 查看定时任务状态 |

## 项目亮点
- 使用 Redis SETNX 实现分布式锁，防止多 Worker 竞争
- Worker 池基于 Goroutine 实现高并发任务处理
- 支持 Cron 表达式定时创建任务
- 全程使用 Cursor AI 辅助开发

## 作者
- 信息与计算科学专业大四学生
- 求职方向：后端开发实习生