@echo off
chcp 65001 >nul
setlocal

:: ==========================================
:: 1. 定义绝对路径 (避免相对路径的混乱)
:: ==========================================
:: %~dp0 是 scripts 目录
set SCRIPT_DIR=%~dp0
:: 切换到项目根目录获取绝对路径
pushd "%SCRIPT_DIR%.."
set PROJECT_ROOT=%cd%
popd

set GOOS=windows
set GOARCH=amd64
set CGO_ENABLED=0

:: 定义各个关键文件的绝对路径
set CLIENT_DIR=%PROJECT_ROOT%\cmd\client
set DIST_DIR=%PROJECT_ROOT%\dist
set ICON=%PROJECT_ROOT%\assets\app.ico
set MANIFEST=%PROJECT_ROOT%\scripts\app.manifest
:: 关键：资源文件必须生成在 main.go 旁边
set SYSO_TARGET=%CLIENT_DIR%\resource.syso
set EXE_OUTPUT=%DIST_DIR%\easytun.exe

:: ==========================================
:: 2. 生成资源文件 (直接写入 cmd/client)
:: ==========================================
echo [1/4] 正在生成资源文件...
echo    - Manifest: %MANIFEST%
echo    - Icon:     %ICON%
echo    - Target:   %SYSO_TARGET%

:: 检查 rsrc 是否存在
where rsrc >nul 2>nul
if %ERRORLEVEL% neq 0 go install github.com/akavel/rsrc@latest

:: 确保清理旧文件
if exist "%SYSO_TARGET%" del /q "%SYSO_TARGET%"

:: 生成 .syso
rsrc -manifest "%MANIFEST%" -ico "%ICON%" -o "%SYSO_TARGET%"

if %ERRORLEVEL% neq 0 (
    echo [错误] 资源生成失败！
    goto :ERROR
)

:: ==========================================
:: 3. 编译逻辑 (模拟手动进入目录)
:: ==========================================
echo [2/4] 清理旧产物...
if not exist "%DIST_DIR%" mkdir "%DIST_DIR%"
if exist "%EXE_OUTPUT%" del /q "%EXE_OUTPUT%"

echo [3/4] 编译...

pushd "%CLIENT_DIR%"

echo 当前工作目录: %cd%
go build -tags client -ldflags "-s -w" -o "%EXE_OUTPUT%" .

if %ERRORLEVEL% neq 0 (
    popd
    echo [错误] 编译失败！
    goto :ERROR
)

:: 编译完成后切回原目录
popd

echo [4/4] 编译成功！

:CLEANUP
if exist "%SYSO_TARGET%" del /q "%SYSO_TARGET%"
echo.
echo 按任意键退出...
pause > nul
exit /b 0

:ERROR
echo.
echo [严重错误] 脚本执行中断。
goto :CLEANUP