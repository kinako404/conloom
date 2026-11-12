# 开发

ConLoom 是一个 Go module（`github.com/kinako404/conloom`），只有一个可执行入口 `cmd/conloom`。代码结构和模块职责见 [架构](architecture.md)。

## 构建与测试

需要 Go 1.26 或更高版本（`go.mod` 为 `go 1.26.0`，由纯 Go 的 SQLite 驱动决定）。

```sh
go vet ./...
CGO_ENABLED=1 go test -race ./...                                  # -race 需要 cgo 与 C 编译器
CGO_ENABLED=0 GOOS=windows GOARCH=arm64 go build ./cmd/conloom     # 发布构建一律不用 cgo
```

- 发布构建一律 `CGO_ENABLED=0`，交叉编译不需要 C 编译器；只有 `go test -race` 需要 cgo。
- 版本和提交号在构建时通过 `-ldflags "-X main.version=… -X main.commit=…"` 注入，`conloom version` 显示它们；本地构建显示 `dev`。
- 核心包（core、gateway、store、policy 等）不能出现平台或模型厂商名，也不能依赖 `internal/adapter`，由 `internal/app/arch_test.go` 检查。

## 基准

在 Intel Xeon E5-2680 v4 @ 2.40GHz（56 线程，linux/amd64）上运行 `go test -run '^$' -bench . -benchtime 2s ./internal/app/` 的原始结果：

```text
goos: linux
goarch: amd64
pkg: github.com/kinako404/conloom/internal/app
cpu: Intel(R) Xeon(R) CPU E5-2680 v4 @ 2.40GHz
BenchmarkIngestObserve-56    	    3386	    730560 ns/op	   28351 B/op	     393 allocs/op
BenchmarkMentionToAck-56     	     439	   5386188 ns/op	  316897 B/op	    4327 allocs/op
PASS
ok  	github.com/kinako404/conloom/internal/app	6.692s
```

两项都是经过真实 Gateway、SQLite（WAL，磁盘临时目录）和 Go 客户端的完整 HTTP 往返：

- `BenchmarkIngestObserve` 并行提交只落库、不回复的群消息，约 0.73 ms/条（所有写入由 SQLite 单写连接串行化，约 1.4k 条/秒）。
- `BenchmarkMentionToAck` 顺序测量一条 @ 机器人的消息从提交到回复被发件箱拉取并确认的全过程，约 5.4 ms，其中模型是立即返回的假服务、合并窗口为 0。

真实环境中的回复延迟主要取决于模型本身（通常为秒级）。

## CI 与发布

- **CI**（`.github/workflows/ci.yml`）：每次推送和 PR 运行 `go vet` 与 `CGO_ENABLED=1 go test -race ./...`，并以 `CGO_ENABLED=0` 交叉编译 linux / darwin / windows × amd64 / arm64 六个目标。
- **发布**（`.github/workflows/release.yml`、`.goreleaser.yaml`）：推送 `v*` 标签时，GitHub Actions 用 GoReleaser 构建六个平台的压缩包（`-trimpath -s -w`，注入版本和提交号）与 `checksums.txt`，发布到 Releases。压缩包为 tar.gz（Windows 为 zip），包含二进制、`README.md`、`LICENSE`、`conloom.example.yaml`、`deploy/` 和 `docs/`。
