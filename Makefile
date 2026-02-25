# ==========================================
# 全局变量与配置
# ==========================================
DIST_DIR := dist
REGISTRY := dplcz666

# --- 服务端配置 ---
SERVER_BIN   := $(DIST_DIR)/easytun-server
SERVER_MAIN  := ./cmd/server/main.go
SERVER_TAGS  := server
DOCKER_IMAGE := easytun-server

# --- 客户端配置 ---
CLIENT_BIN   := $(DIST_DIR)/easytun.exe
CLIENT_DIR   := ./cmd/client
CLIENT_TAGS  := client
MANIFEST     := ./scripts/app.manifest
ICON         := ./assets/app.ico
SYSO_TARGET  := $(CLIENT_DIR)/resource.syso

# ==========================================
# 核心目标
# ==========================================

.PHONY: all server client docker clean

# 默认构建所有二进制文件（不包含 Docker 推送）
all: server client

# ==========================================
# 1. 构建 Linux 服务端二进制
# ==========================================
server:
	@echo "==> [1/3] 正在编译服务端 (Target: linux/amd64)..."
	@mkdir -p $(DIST_DIR)
	@rm -f $(SERVER_BIN)
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -tags $(SERVER_TAGS) -ldflags "-s -w" -o $(SERVER_BIN) $(SERVER_MAIN)
	@echo "==> 服务端编译完成: $(SERVER_BIN)"

# ==========================================
# 2. 构建并推送 Docker 镜像 (依赖 server 构建)
# ==========================================
docker: server
	@echo "==> [2/3] 正在构建 Docker 基础镜像 (latest)..."
	docker build -t $(DOCKER_IMAGE):latest .

	@echo "==> 计算新版本号..."
	$(eval NEW_TAG := $(shell powershell -NoProfile -Command "$$tags = docker images --format '{{.Tag}}' $(DOCKER_IMAGE) | Where-Object { $$_ -match '^v\d+\.\d+' }; if ($$tags) { $$latest = $$tags | Sort-Object { [version]($$_ -replace '^v','') } -Descending | Select-Object -First 1; $$parts = ($$latest -replace '^v','').Split('.'); $$parts[-1] = [int]$$parts[-1] + 1; 'v' + ($$parts -join '.') } else { 'v1.0' }"))
	@echo "==> 分配的新版本号: $(NEW_TAG)"

	docker tag $(DOCKER_IMAGE):latest $(DOCKER_IMAGE):$(NEW_TAG)

	@if [ "$(REGISTRY)" != "" ]; then \
		echo "==> 正在打远程标签并推送至: $(REGISTRY)..."; \
		docker tag $(DOCKER_IMAGE):latest $(REGISTRY)/$(DOCKER_IMAGE):$(NEW_TAG); \
		docker tag $(DOCKER_IMAGE):latest $(REGISTRY)/$(DOCKER_IMAGE):latest; \
		docker push $(REGISTRY)/$(DOCKER_IMAGE):$(NEW_TAG); \
		docker push $(REGISTRY)/$(DOCKER_IMAGE):latest; \
	else \
		echo "==> 未配置 REGISTRY，跳过远程推送。"; \
	fi
	@echo "==> Docker 构建与打标流程完成！"

# ==========================================
# 3. 构建 Windows 客户端二进制 (包含图标和 UAC 提权声明)
# ==========================================
client:
	@echo "==> [3/3] 正在准备客户端资源文件..."
	@if ! command -v rsrc >/dev/null 2>&1; then \
		echo "    - 未检测到 rsrc，正在安装..."; \
		go install github.com/akavel/rsrc@latest; \
	fi
	@rm -f $(SYSO_TARGET)
	rsrc -manifest $(MANIFEST) -ico $(ICON) -o $(SYSO_TARGET)

	@echo "==> 正在编译客户端 (Target: windows/amd64)..."
	@mkdir -p $(DIST_DIR)
	@rm -f $(CLIENT_BIN)
	GOOS=windows GOARCH=amd64 go build -tags $(CLIENT_TAGS) -ldflags "-s -w" -o $(CLIENT_BIN) $(CLIENT_DIR)

	@echo "==> 清理临时资源文件..."
	@rm -f $(SYSO_TARGET)
	@echo "==> 客户端编译完成: $(CLIENT_BIN)"

# ==========================================
# 4. 清理构建产物
# ==========================================
clean:
	@echo "==> 正在清理所有构建产物..."
	@rm -rf $(DIST_DIR)
	@rm -f $(SYSO_TARGET)
	@echo "==> 清理完毕。"