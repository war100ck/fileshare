@echo off
setlocal
cd /d "%~dp0"

where go >nul 2>nul
if errorlevel 1 (
    echo Go не найден. Установите: winget install --id GoLang.Go
    pause
    exit /b 1
)

tasklist /fi "imagename eq fileshare.exe" 2>nul | find /i "fileshare.exe" >nul
if not errorlevel 1 (
    echo Останавливаю запущенный fileshare.exe...
    taskkill /f /im fileshare.exe >nul 2>nul
    timeout /t 1 /nobreak >nul
)

echo Сборка fileshare.exe...
go build -o fileshare.exe ./cmd/server
if errorlevel 1 (
    echo ОШИБКА СБОРКИ
    pause
    exit /b 1
)

echo Готово: %~dp0fileshare.exe
echo Запуск: просто запустите fileshare.exe
pause
endlocal