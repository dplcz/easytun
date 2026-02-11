@echo off
chcp 65001 >nul
setlocal

:: ==========================================
:: 自动定位路径
:: ==========================================
:: %~dp0 是当前脚本所在目录 (scripts/)
:: 我们需要进入它的上一级 (项目根目录)
set SCRIPT_DIR=%~dp0
cd /d "%SCRIPT_DIR%.."
set PROJECT_ROOT=%cd%

:: ==========================================
:: 配置区域 (基于项目根目录)
:: ==========================================
set BIN_NAME=client.exe
set MAIN_PATH=./cmd/client/main.go
set OUTPUT_DIR=./dist
set BUILD_TAGS=client
set MANIFEST_PATH=./scripts/app.manifest

:: ==========================================
:: 1. 环境检查与资源处理
:: ==========================================
echo [1/4] 检查工具 rsrc...
where rsrc >nul 2>nul
if %ERRORLEVEL% neq 0 (
    echo 正在安装 rsrc...
    go install github.com/akavel/rsrc@latest
)

echo [2/4] 生成资源文件...
:: 在根目录生成 .syso，这样 go build 能自动识别
rsrc -manifest "%MANIFEST_PATH%" -o "%PROJECT_ROOT%\client_windows.syso"

:: ==========================================
:: 2. 编译逻辑
:: ==========================================
echo [3/4] 清理旧产物...
if not exist "%OUTPUT_DIR%" mkdir "%OUTPUT_DIR%"

echo [4/4] 正在编译客户端 (Tags: %BUILD_TAGS%)...
:: 执行编译
go build -tags %BUILD_TAGS% -ldflags "-s -w" -o "%OUTPUT_DIR%/%BIN_NAME%" "%MAIN_PATH%"

if %ERRORLEVEL% neq 0 (
    echo.
    echo [错误] 编译失败！
    goto :PAUSE
)

echo.
echo ==========================================
echo 编译成功！位置: %OUTPUT_DIR%\%BIN_NAME%
echo ==========================================

:PAUSE
:: 清理生成的临时资源文件
if exist "%PROJECT_ROOT%\client_windows.syso" del /q "%PROJECT_ROOT%\client_windows.syso"
echo.
echo 按任意键退出...
pause > nul