@echo off
rem Builds dist\twitchminer.exe for Windows with the app icon and version
rem details embedded, compresses it with UPX and packs it into
rem dist\TwitchMiner-<version>-windows-<arch>.rar with WinRAR. Either step
rem is skipped, with a note, when its tool isn't installed.
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

rem UPX shrinks the exe to about a third. Some antivirus programs distrust
rem UPX-packed files; if one flags the exe, build without UPX installed.
where upx >nul 2>nul
if errorlevel 1 (
    echo   - skipping UPX: it isn't installed or isn't on PATH ^(https://upx.github.io^)
    goto :upx_done
)
echo   - compressing with UPX
upx --best --lzma -q dist\twitchminer.exe >nul
if errorlevel 1 echo     UPX couldn't compress this build; keeping the uncompressed exe.
:upx_done

rem WinRAR's console tool usually isn't on PATH, so look in its install
rem folders too.
set "RAR="
for /f "delims=" %%R in ('where rar 2^>nul') do if not defined RAR set "RAR=%%R"
if not defined RAR if exist "%ProgramFiles%\WinRAR\Rar.exe" set "RAR=%ProgramFiles%\WinRAR\Rar.exe"
if not defined RAR if exist "%ProgramFiles(x86)%\WinRAR\Rar.exe" set "RAR=%ProgramFiles(x86)%\WinRAR\Rar.exe"
set "ARCHIVE="
if not defined RAR (
    echo   - skipping the .rar: WinRAR isn't installed ^(https://www.win-rar.com^)
    goto :rar_done
)
set "ARCHIVE=dist\TwitchMiner-%VERSION%-windows-%ARCH%.rar"
echo   - packing %ARCHIVE%
if exist "%ARCHIVE%" del /q "%ARCHIVE%"
"%RAR%" a -ep1 -m5 -idq -y "%ARCHIVE%" dist\twitchminer.exe
if errorlevel 1 (
    echo.
    echo Packing the .rar failed.
    exit /b 1
)
:rar_done

echo.
echo Done: %CD%\dist\twitchminer.exe
if defined ARCHIVE echo       %CD%\%ARCHIVE%
exit /b 0

:cleanup
del /q cmd\twitchminer\rsrc_windows_*.syso 2>nul
exit /b 0

:fail
call :cleanup
echo.
echo Build failed.
exit /b 1
