@echo off
chcp 65001 >nul
setlocal

set SCRIPT_DIR=%~dp0
cd /d "%SCRIPT_DIR%.."
set PROJECT_ROOT=%cd%

:: ==========================================
:: 配置区域
:: ==========================================
set BIN_NAME=easytun-server
set MAIN_PATH=./cmd/server/main.go
set OUTPUT_DIR=./dist
:: 如果你的服务端有特殊的 build tags，可以在此添加
set BUILD_TAGS=server

set GOOS=linux
set GOARCH=amd64
set CGO_ENABLED=0

:: [新增] 镜像与仓库配置
:: 如果你有远程仓库（如 Docker Hub、阿里云或私有 Harbor），请在这里配置前缀
:: 例如: set REGISTRY=registry.cn-hangzhou.aliyuncs.com/my-namespace
:: 如果只想留在本地，留空即可: set REGISTRY=
set REGISTRY=dplcz666

echo [1/4] 正在清理旧的 Linux 编译产物...
if not exist "%OUTPUT_DIR%" mkdir "%OUTPUT_DIR%"
if exist "%OUTPUT_DIR%\%BIN_NAME%" del /q "%OUTPUT_DIR%\%BIN_NAME%"

echo [2/4] 正在进行交叉编译 (Target: %GOOS%/%GOARCH%)...
go build -tags %BUILD_TAGS% -ldflags "-s -w" -o "%OUTPUT_DIR%/%BIN_NAME%" "%MAIN_PATH%"

if %ERRORLEVEL% neq 0 (
    echo.
    echo [错误] 交叉编译失败，请检查上方报错信息。
    goto :PAUSE
)

echo [3/4] 正在构建 Docker 基础镜像 (latest)...
docker build -t %BIN_NAME%:latest .
if %ERRORLEVEL% neq 0 (
    echo.
    echo [错误] 构建 Docker 镜像失败，请检查上方报错信息。
    goto :PAUSE
)

echo [4/4] 正在计算新版本号并处理镜像标签...

:: 使用内嵌 PowerShell 获取当前 Docker 中符合 vX.X 格式的最新版本并自动 +1
:: 如果没有任何历史版本，默认返回 v1.0
for /f "usebackq tokens=*" %%i in (`powershell -NoProfile -Command "$uri = 'https://hub.docker.com/v2/repositories/%REGISTRY%/%BIN_NAME%/tags/?page_size=100'; try { $tags = (Invoke-RestMethod -Uri $uri -ErrorAction Stop).results.name | Where-Object { $_ -match '^v\d+\.\d+' }; if ($tags) { $latest = $tags | Sort-Object { [version]($_ -replace '^v','') } -Descending | Select-Object -First 1; $parts = ($latest -replace '^v','').Split('.'); $parts[-1] = [int]$parts[-1] + 1; 'v' + ($parts -join '.') } else { 'v1.0' } } catch { 'v1.0' }"`) do set NEW_TAG=%%i
echo ==========================================
echo 自动分配的新版本号: %NEW_TAG%
echo ==========================================

:: 1. 给本地打上带版本号的 tag
docker tag %BIN_NAME%:latest %BIN_NAME%:%NEW_TAG%

:: 2. 如果配置了远程仓库，则打上远程 tag 并推送
if not "%REGISTRY%"=="" (
    echo 正在打远程标签...
    docker tag %BIN_NAME%:latest %REGISTRY%/%BIN_NAME%:%NEW_TAG%
    docker tag %BIN_NAME%:latest %REGISTRY%/%BIN_NAME%:latest

    echo 正在推送到远程仓库: %REGISTRY%/%BIN_NAME%
    docker push %REGISTRY%/%BIN_NAME%:%NEW_TAG%
    docker push %REGISTRY%/%BIN_NAME%:latest

    if %ERRORLEVEL% neq 0 (
        echo.
        echo [错误] 镜像推送失败，请检查网络或登录状态 ^(docker login^)。
        goto :PAUSE
    )
) else (
    echo 未配置远程仓库变量 REGISTRY，跳过推送步骤。
)

echo.
echo ==========================================
echo 构建、标记与推送流程已全部完成！
echo 最新镜像: %BIN_NAME%:%NEW_TAG%
echo ==========================================

:PAUSE
echo.
echo 按任意键退出脚本...
pause > nul