@echo off
REM opencode2api launcher script
REM Uses OpenCode Zen API key and V2 server to provide free model access
REM Tool calls are NOT executed on the proxy server - they are forwarded to the client

REM Configuration
set OPENCODE_API_KEY=oc_sk_103d308fe5d0_vc-IekjPQ_ZLnPH7XHJC8jTTdyY4QaDP
set V2_SERVER_URL=http://127.0.0.1:49374
set LISTEN_PORT=3000

REM Check if opencode service is running
call opencode service status >nul 2>&1
if %errorlevel% neq 0 (
    echo Starting OpenCode service...
    call opencode service start
    call :wait_service
)

echo ================================================
echo   OpenCode2API Proxy
echo ================================================
echo   API Key: %OPENCODE_API_KEY:~0,8%...%OPENCODE_API_KEY:~-4%
echo   V2 Server: %V2_SERVER_URL%
echo   Listen Port: %LISTEN_PORT%
echo   Tools Disabled: Yes (builtAgent:false)
echo ================================================

REM Build and run opencode2api
cd /d C:\github\opencode2api

echo Starting opencode2api on port %LISTEN_PORT%...
echo OpenCode free models are now accessible via OpenAI-compatible API!
echo Press Ctrl+C to stop
echo ================================================

go run ./cmd/opencode2api -config config.json

pause

goto :eof

:wait_service
echo Waiting for OpenCode service to start...
setlocal enabledelayedexpansion
for /l %%i in (1,1,30) do (
    call opencode service status >nul 2>&1
    if !errorlevel! equ 0 (
        echo OpenCode service started
        goto :eof
    )
    timeout /t 2 /nobreak >nul
)
echo Warning: OpenCode service may not have started properly
endlocal
goto :eof
