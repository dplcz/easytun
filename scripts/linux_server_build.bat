@echo off
chcp 65001 >nul
setlocal


set SCRIPT_DIR=%~dp0
cd /d "%SCRIPT_DIR%.."
set PROJECT_ROOT=%cd%

:: ==========================================
:: 配置区域
:: ==========================================
set BIN_NAME=server
set MAIN_PATH=./cmd/server/main.go
set OUTPUT_DIR=./dist
:: 如果你的服务端有特殊的 build tags，可以在此添加
set BUILD_TAGS=server

set GOOS=linux
set GOARCH=amd64
set CGO_ENABLED=0
set DOCKER_IMAGE_NAME=wintun-server:latest

echo [1/3] 正在清理旧的 Linux 编译产物...
if not exist "%OUTPUT_DIR%" mkdir "%OUTPUT_DIR%"
if exist "%OUTPUT_DIR%\%BIN_NAME%" del /q "%OUTPUT_DIR%\%BIN_NAME%"

echo [2/3] 正在进行交叉编译 (Target: %GOOS%/%GOARCH%)...

go build -tags %BUILD_TAGS% -ldflags "-s -w" -o "%OUTPUT_DIR%/%BIN_NAME%" "%MAIN_PATH%"

if %ERRORLEVEL% neq 0 (
    echo.
    echo [错误] 交叉编译失败，请检查上方报错信息。
    goto :PAUSE
)

echo [3/3] 正在构建 Docker 镜像...
docker build -t %DOCKER_IMAGE_NAME% .
if %ERRORLEVEL% neq 0 (
    echo.
    echo [错误] 构建 Docker 镜像失败，请检查上方报错信息。
    goto :PAUSE
)

echo.
echo ==========================================
echo 镜像构建完成： %DOCKER_IMAGE_NAME%
echo ==========================================

:PAUSE
echo.
echo 按任意键退出脚本...
pause > nul