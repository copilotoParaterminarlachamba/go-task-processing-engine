# TaskForge — Concurrent Task Processing Engine (Go)

![Go](https://img.shields.io/badge/go-%3E%3D1.21-blue)
![Concurrency](https://img.shields.io/badge/concurrency-goroutines-green)
![Worker Pool](https://img.shields.io/badge/pattern-worker--pool-purple)
![Priority Queue](https://img.shields.io/badge/queue-priority-orange)
![HTTP API](https://img.shields.io/badge/api-net%2Fhttp-lightgrey)
![Metrics](https://img.shields.io/badge/metrics-prometheus--style-brightgreen)
![Render](https://img.shields.io/badge/render-deployed-red)

**TaskForge** is a **concurrent task processing engine** written in **Go**, demonstrating production-grade backend patterns:
worker pools, priority queues, retry logic, graceful shutdown, observability, and a RESTful HTTP API.

This project is designed as:
- a **hands-on reference** for mastering Go concurrency patterns
- a **portfolio-ready backend project** for technical interviews
- a **foundation** for background job systems and internal task runners

---

## ⚠️ Disclaimer

TaskForge is a **single-node, in-memory** task engine.

It is **not production-ready out of the box** and intentionally avoids external dependencies
(Redis, Kafka, RabbitMQ, databases) to keep the core concepts explicit and readable.

---

## Features

### Core Task Processing

#### Priority Queue

- Thread-safe priority queue based on `container/heap`
- Ordering rules:
  - Higher priority processed first (`critical > high > normal > low`)
  - FIFO ordering within the same priority level
- Bounded capacity with backpressure (`ErrQueueFull`)

#### Worker Pool

- Configurable number of concurrent workers
- Controlled parallelism using goroutines
- Graceful shutdown with context cancellation
- Panic-safe task execution (workers never crash)

#### Task Lifecycle

Tasks move through a clear state machine:

```
pending → running → completed
    ↑          ↘ failed
    |              ↓
    +←←← retry ←←←+
    
    ↘ cancelled (at any point)
```

Supported states:
- `pending`
- `running`
- `completed`
- `failed`
- `cancelled`

#### Retry Logic

- Per-task retry limits
- Unlimited retries supported (`max_retries = -1`)
- Retry state tracked inside the task domain model
- Safe re-queueing on transient failures

---

### HTTP API

- Built using Go standard library (`net/http`)
- RESTful design
- JSON request/response handling
- Strict request validation
- Graceful shutdown support

#### Key Endpoints

| Method | Path | Description |
|------|------|------------|
| GET | `/` | Web UI (static HTML) |
| POST | `/api/v1/tasks` | Create a new task |
| GET | `/api/v1/tasks` | List tasks |
| GET | `/api/v1/tasks/{id}` | Get task by ID |
| DELETE | `/api/v1/tasks/{id}` | Delete task |
| GET | `/api/v1/stats` | System statistics |
| GET | `/health` | Health check |
| GET | `/metrics` | Prometheus-style metrics |

---

### Observability

#### Metrics Collected

- Total tasks created
- Successfully processed tasks
- Failed tasks
- Average task processing latency
- HTTP request counts by endpoint and status
- Current queue size

Metrics are exposed in **Prometheus-compatible format**.

#### Logging

- Structured, timestamped logs
- Per-worker visibility
- Task lifecycle events
- Panic stack traces for debugging

---

## Supported Built-in Task Types

| Task Type | Description |
|---------|------------|
| `echo` | Returns the payload unchanged |
| `sleep` | Sleeps for a specified duration |
| `compute` | CPU-bound simulated computation |
| `fail` | Always fails (testing retries & errors) |

Custom task handlers can be registered programmatically.

---

## Project Structure

```
go-task-processing-engine/
├── main.go
├── go.mod
├── go.sum
├── render.yaml
├── internal/
│   ├── api/
│   │   └── server.go
│   ├── queue/
│   │   └── priority_queue.go
│   ├── storage/
│   │   └── memory.go
│   ├── task/
│   │   └── task.go
│   └── worker/
│       └── pool.go
├── pkg/
│   └── metrics/
│       └── collector.go
└── web/
    └── index.html
```

---

## Installation & Running

### 1. Clone the repository

```bash
git clone https://github.com/smart-developer1791/go-task-processing-engine
cd go-task-processing-engine
```

### 2. Run the server

```bash
go run ./...
```

### 3. Open in browser

```
http://localhost:8080
```

---

## API Usage Examples

### Create a task

```bash
curl -X POST http://localhost:8080/api/v1/tasks \
  -H "Content-Type: application/json" \
  -d '{
    "type": "sleep",
    "payload": { "duration": "2s" },
    "priority": 2,
    "max_retries": 3
  }'
```

Example response:

```json
{
  "id": "ad24bc74-639e-42e6-8666-91387bd2ad23",
  "type": "sleep",
  "payload": { "duration": "2s" },
  "priority": 2,
  "status": "pending",
  "created_at": "2025-12-29T00:25:57Z",
  "retry_count": 0,
  "max_retries": 3
}
```

### Get task status

```bash
curl http://localhost:8080/api/v1/tasks/{task_id}
```

### List tasks

```bash
curl http://localhost:8080/api/v1/tasks
```

---

## Health Check

```bash
curl http://localhost:8080/health
```

Example response:

```json
{
  "status": "healthy",
  "timestamp": "2025-12-29T00:27:16Z",
  "queue": {
    "size": 2,
    "closed": false
  }
}
```

---

## Design Principles

- **Explicit concurrency** over magic abstractions
- **Domain-driven task model**
- **Fail-fast validation**
- **Panic-safe execution**
- **Graceful shutdown**
- **Clear separation of concerns**

The code prioritizes **readability and correctness** over premature optimization.

---

## Production Considerations

To adapt TaskForge for production use:

- Replace in-memory queue with Redis / Kafka / NATS
- Add persistent storage for crash recovery
- Implement delayed retries with exponential backoff
- Add task timeouts and cancellation propagation
- Add authentication & authorization
- Export structured logs (Zap / Zerolog)
- Deploy behind a load balancer
- Add distributed tracing (OpenTelemetry)

---

## Purpose

TaskForge exists as:

- a **reference Go concurrency project**
- a **background job engine blueprint**
- a **portfolio-grade backend system**
- an **interview discussion artifact**

---

## Deploy in 10 seconds

[![Deploy to Render](https://render.com/images/deploy-to-render-button.svg)](https://render.com/deploy)
