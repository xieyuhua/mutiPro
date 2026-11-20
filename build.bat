@echo off
chcp 65001 >nul
setlocal

REM ============================================================
REM  MultiPro 构建脚本
REM  产物：dist\MultiPro.exe —— 单个原生 exe，不依赖 .NET 运行时
REM ============================================================

where go >nul 2>&1
if errorlevel 1 (
    echo 找不到 Go，请先从 https://go.dev/dl/ 安装 Go 1.21+
    exit /b 1
)

REM 生成清单资源文件（已随仓库提供；manifest 改动后重新生成即可）
if not exist gosrc\rsrc_windows_amd64.syso (
    echo 正在生成清单资源...
    go run github.com/akavel/rsrc@latest -manifest gosrc\app.manifest -o gosrc\rsrc_windows_amd64.syso
    if errorlevel 1 (
        echo 生成清单资源失败
        exit /b 1
    )
)

if not exist dist mkdir dist

pushd gosrc
set GOOS=windows
set GOARCH=amd64
set CGO_ENABLED=0
go build -trimpath -ldflags "-H=windowsgui -s -w" -o ..\dist\MultiPro.exe .
set BUILD_RESULT=%errorlevel%
popd

if not %BUILD_RESULT%==0 (
    echo.
    echo 构建失败
    exit /b 1
)

echo.
echo 构建成功：dist\MultiPro.exe
