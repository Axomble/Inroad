<#
.SYNOPSIS
  Start the whole Inroad dev stack: Postgres + Redis, migrations, API, worker, SPA.

.DESCRIPTION
  The Windows equivalent of `make dev`. It exists because make is not installed on
  a default Windows box, so the documented "cp .env.example .env && make run-api"
  flow is not actually available there. It also handles the two things that bite
  every time on this platform:

    * Go is installed but not on the default PATH, so `go run` reports
      "command not found" until you fix it.
    * Nothing in the Go code reads .env, so the API exits with
      "INROAD_JWT_SECRET must be set" even though the file is sitting right there.

  Each service gets its own window: they are long-running, and interleaving three
  logs in one console makes the one you need unreadable. Closing a window stops
  that service.

  ASCII only, deliberately: Windows PowerShell 5.1 reads a script without a BOM
  as ANSI, so a stray em dash or arrow becomes a parser error rather than a
  character.

.PARAMETER SetupOnly
  Bring up the database and apply migrations, then stop. Nothing is launched.

.PARAMETER SkipWeb
  Start the API and worker but not the SPA dev server.

.EXAMPLE
  .\scripts\dev.ps1
#>
[CmdletBinding()]
param(
  [switch]$SetupOnly,
  [switch]$SkipWeb
)

$ErrorActionPreference = 'Stop'
$repo = Split-Path -Parent $PSScriptRoot
Set-Location $repo

function Add-GoToPath {
  foreach ($p in @("$env:ProgramFiles\Go\bin", "$env:USERPROFILE\go\bin")) {
    if ((Test-Path $p) -and ($env:PATH -notlike "*$p*")) { $env:PATH += ";$p" }
  }
  if (-not (Get-Command go -ErrorAction SilentlyContinue)) {
    throw "go not found. Install Go, or add its bin directory to PATH."
  }
}

function Import-DotEnv {
  if (-not (Test-Path .env)) {
    throw ".env not found. Run: Copy-Item .env.example .env  and fill in INROAD_JWT_SECRET and INROAD_MASTER_KEY."
  }
  # KEY=VALUE only; comments and blank lines are skipped. The value is taken
  # verbatim so a DSN containing '=' in a query parameter survives intact.
  # Set at process scope, which every child process inherits.
  Get-Content .env | ForEach-Object {
    if ($_ -match '^\s*([^#=\s][^=]*)=(.*)$') {
      [Environment]::SetEnvironmentVariable($Matches[1].Trim(), $Matches[2].Trim())
    }
  }
  foreach ($required in @('INROAD_DATABASE_URL', 'INROAD_JWT_SECRET', 'INROAD_MASTER_KEY')) {
    if (-not [Environment]::GetEnvironmentVariable($required)) {
      throw "$required is empty in .env. The API refuses to start without it."
    }
  }
}

function Start-Services {
  Write-Host "starting postgres + redis..." -ForegroundColor Cyan
  docker compose -f deploy/compose/docker-compose.dev.yml up -d | Out-Null
  if ($LASTEXITCODE -ne 0) { throw "docker compose failed. Is Docker Desktop running?" }

  Write-Host "waiting for postgres..." -NoNewline
  foreach ($i in 1..30) {
    docker compose -f deploy/compose/docker-compose.dev.yml exec -T postgres pg_isready -U inroad -q 2>$null
    if ($LASTEXITCODE -eq 0) { Write-Host " ready"; return }
    Start-Sleep -Seconds 1
    Write-Host "." -NoNewline
  }
  throw "postgres did not become ready in 30s."
}

function Invoke-Migrations {
  Write-Host "applying migrations..." -ForegroundColor Cyan
  go run ./cmd/migrate up
  if ($LASTEXITCODE -ne 0) { throw "migrations failed." }
}

# The child inherits this process's environment, so .env values and the patched
# PATH carry over without being re-quoted into the command string.
function Start-InWindow {
  param([string]$Title, [string]$Command)
  $inner = "`$host.UI.RawUI.WindowTitle = '$Title'; Set-Location '$repo'; $Command"
  Start-Process powershell -ArgumentList '-NoExit', '-Command', $inner
}

Add-GoToPath
Import-DotEnv
Start-Services
Invoke-Migrations

if ($SetupOnly) {
  Write-Host ""
  Write-Host "setup complete: database up, migrations applied." -ForegroundColor Green
  return
}

Start-InWindow -Title 'inroad api'    -Command 'go run ./cmd/inroad'
Start-InWindow -Title 'inroad worker' -Command 'go run ./cmd/worker'
if (-not $SkipWeb) {
  Start-InWindow -Title 'inroad web' -Command 'Set-Location web; npm run dev'
}

Write-Host ""
Write-Host "  api  -> http://localhost:8080" -ForegroundColor Green
Write-Host "  web  -> http://localhost:5173" -ForegroundColor Green
Write-Host "  db   -> localhost:5433   (inroad / inroad / inroad)" -ForegroundColor Green
Write-Host ""
Write-Host "  login: demo@inroad.test / demodemo"
Write-Host "  no demo user? run: go run ./cmd/seed"
Write-Host "  Go has no hot reload: restart the api window after changing Go code."
Write-Host ""
