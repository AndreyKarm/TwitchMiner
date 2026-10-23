@echo off
rem Builds dist\twitchminer.exe for Windows with the app icon and version
rem details embedded.
rem
rem   build.bat                 tray app for 64-bit Windows (no console window)
rem   build.bat arm64           the same for Windows on ARM
rem   build.bat console         keep the console window, to watch the log live
rem   build.bat arm64 console   both

setlocal EnableExtensions
cd /d "%~dp0"

set "ARCH=amd64"
set "SUBSYSTEM=-H=windowsgui"
set "MANIFEST=gui"
for %%A in (%*) do (
    if /i "%%~A"=="amd64" set "ARCH=amd64"
    if /i "%%~A"=="arm64" set "ARCH=arm64"
    if /i "%%~A"=="console" (
        set "SUBSYSTEM="
        set "MANIFEST=cli"
    )
)

where go >nul 2>nul
if errorlevel 1 (
    echo Go isn't installed or isn't on PATH. Get it from https://go.dev/dl/
    exit /b 1
)

rem App version, e.g. v0.2.0, v0.2.0-3-gabc1234 or abc1234-dirty.
set "VERSION=dev"
for /f "delims=" %%V in ('git describe --tags --always --dirty 2^>nul') do set "VERSION=%%V"

rem The exe's file version must be numeric: the latest tag without its "v"
rem and any "-rc" suffix, or 0.0.0 when there are no tags yet.
rem (cmd parses a block even when skipping it, so this avoids one: expanding
rem %TAG:v=% with TAG unset breaks the syntax. "=" needs escaping in for /f.)
set "FILEVER=0.0.0"
set "TAG="
for /f "delims=" %%T in ('git describe --tags --abbrev^=0 2^>nul') do set "TAG=%%T"
if not defined TAG goto :version_done
set "TAG=%TAG:v=%"
for /f "tokens=1 delims=-" %%N in ("%TAG%") do set "FILEVER=%%N"
:version_done

echo Building TwitchMiner %VERSION% for windows/%ARCH%...

if not exist build mkdir build
if not exist dist mkdir dist

echo   - drawing the icon
go run ./tools/genicon build\twitchminer.ico || goto :fail

echo   - embedding the icon and version details
go run github.com/tc-hib/go-winres@v0.3.3 simply ^
    --arch %ARCH% ^
    --out cmd\twitchminer\rsrc ^
    --icon build\twitchminer.ico ^
    --manifest %MANIFEST% ^
    --product-name TwitchMiner ^
    --file-description TwitchMiner ^
    --original-filename twitchminer.exe ^
    --product-version %FILEVER% ^
    --file-version %FILEVER% || goto :fail

echo   - compiling
set "GOOS=windows"
set "GOARCH=%ARCH%"
set "CGO_ENABLED=0"
go build -trimpath -ldflags "-s -w %SUBSYSTEM% -X github.com/AndreyKarm/TwitchMiner/internal/config.Version=%VERSION%" -o dist\twitchminer.exe ./cmd/twitchminer
if errorlevel 1 (
    call :cleanup
    echo.
    echo Compiling failed. If TwitchMiner is running, quit it from the tray and try again.
    exit /b 1
)
call :cleanup

echo.
echo Done: %CD%\dist\twitchminer.exe
exit /b 0

:cleanup
del /q cmd\twitchminer\rsrc_windows_*.syso 2>nul
exit /b 0

:fail
call :cleanup
echo.
echo Build failed.
exit /b 1
