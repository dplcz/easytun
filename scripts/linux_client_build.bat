@echo off
chcp 65001 >nul
setlocal

:: ==========================================
:: 1. 定义路径
:: ==========================================
set SCRIPT_DIR=%~dp0
pushd "%SCRIPT_DIR%.."
set PROJECT_ROOT=%cd%
popd

set CLIENT_DIR=%PROJECT_ROOT%\cmd\client
set DIST_DIR=%PROJECT_ROOT%\dist
set OUTPUT_BIN=%DIST_DIR%\easytun-linux

:: ==========================================
:: 2. 准备环境
:: ==========================================
echo [1/3] 正在清理并准备输出目录...
if not exist "%DIST_DIR%" mkdir "%DIST_DIR%"
if exist "%OUTPUT_BIN%" del /q "%OUTPUT_BIN%"

:: ==========================================
:: 3. 交叉编译逻辑
:: ==========================================
echo [2/3] 正在交叉编译 Linux 客户端 (Target: linux/amd64)...

:: 设置交叉编译环境变量
set GOOS=linux
set GOARCH=amd64
set CGO_ENABLED=0

pushd "%CLIENT_DIR%"

:: 执行编译
:: -tags client: 触发 Linux 特有的代码实现
:: -ldflags "-s -w": 压缩二进制体积，移除调试符号
go build -tags client -ldflags "-s -w" -o "%OUTPUT_BIN%" .

if %ERRORLEVEL% neq 0 (
    popd
    echo [错误] 编译失败！
    goto :ERROR
)

popd

echo [3/3] 编译成功！
echo 输出文件: %OUTPUT_BIN%

:END
echo.
echo 按任意键退出...
pause > nul
exit /b 0

:ERROR
echo.
echo [严重错误] 脚本执行中断。
pause > nul
exit /b 1
