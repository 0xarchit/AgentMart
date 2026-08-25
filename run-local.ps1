$ErrorActionPreference = "Stop"
$root = Split-Path -Parent $MyInvocation.MyCommand.Path
Set-Location $root

function Import-DotEnv([string]$path) {
  if (-not (Test-Path $path)) { throw "Missing $path. Copy .env.example to .env and fill required values." }
  foreach ($line in Get-Content $path) {
    if ($line -match '^\s*#' -or $line -match '^\s*$') { continue }
    if ($line -match '^\s*([^=]+)=(.*)$') {
      $name = $matches[1].Trim()
      $value = $matches[2].Trim()
      if ($value.Length -ge 2 -and (($value.StartsWith('"') -and $value.EndsWith('"')) -or ($value.StartsWith("'") -and $value.EndsWith("'")))) {
        $value = $value.Substring(1, $value.Length - 2)
      }
      [Environment]::SetEnvironmentVariable($name, $value, "Process")
    }
  }
}
Import-DotEnv (Join-Path $root ".env")

$market = if ($env:MARKET_HTTP_PORT) { $env:MARKET_HTTP_PORT } else { "8081" }
$env:MARKET_ADDR = ":$market"
$env:MARKET_AGENT_CARD_URL = "http://localhost:$market/a2a/"
$env:USER_MARKET_MCP_ENDPOINT = "http://localhost:$market/mcp"
$env:USER_MARKET_A2A_ENDPOINT = "http://localhost:$market/a2a/.well-known/agent-card.json"
$env:NEXT_PUBLIC_APP_URL = if ($env:NEXT_PUBLIC_APP_URL) { $env:NEXT_PUBLIC_APP_URL } else { "http://localhost:3000" }

Write-Host "building market and user..."
go build -o bin/market.exe ./cmd/market
if ($LASTEXITCODE -ne 0) { throw "market build failed" }
go build -o bin/user.exe ./cmd/user
if ($LASTEXITCODE -ne 0) { throw "user build failed" }

$processes = @()
try {
  Write-Host "starting market on http://localhost:$market (MCP: /mcp, A2A: /a2a)"
  $marketProcess = Start-Process -FilePath "$root/bin/market.exe" -WorkingDirectory $root -NoNewWindow -PassThru
  $processes += $marketProcess

  $healthy = $false
  foreach ($attempt in 1..20) {
    if ($marketProcess.HasExited) { throw "market exited early with code $($marketProcess.ExitCode)" }
    try {
      $health = Invoke-RestMethod "http://localhost:$market/health" -TimeoutSec 1
      if ($health.status -eq "ok") { $healthy = $true; break }
    } catch { Start-Sleep -Milliseconds 500 }
  }
  if (-not $healthy) { throw "market did not become healthy within 10 seconds" }
  Write-Host "market healthy; starting Telegram user agent"

  $userProcess = Start-Process -FilePath "$root/bin/user.exe" -WorkingDirectory $root -NoNewWindow -PassThru
  $processes += $userProcess
  Start-Sleep -Seconds 1
  if ($userProcess.HasExited) { throw "user exited early with code $($userProcess.ExitCode)" }

  Write-Host "starting web on http://localhost:3000"

  # Expose the local web server so Razorpay webhooks can reach the API routes.
  # Register the printed URL in the Razorpay dashboard (Webhooks -> payment.captured,
  # secret = RAZORPAY_WEBHOOK_SECRET from .env) and wallet top-ups credit live.
  if (Get-Command cloudflared -ErrorAction SilentlyContinue) {
    $tunnelLog = Join-Path $env:TEMP "agentmart-tunnel.log"
    Set-Content -Path $tunnelLog -Value "" 
    $tunnelProcess = Start-Process -FilePath "cloudflared" -ArgumentList "tunnel", "--url", "http://localhost:3000" `
      -NoNewWindow -PassThru -RedirectStandardOutput $tunnelLog -RedirectStandardError "$tunnelLog.err"
    $processes += $tunnelProcess
    $webhookURL = $null
    foreach ($attempt in 1..60) {
      Start-Sleep -Milliseconds 500
      $logText = (Get-Content $tunnelLog, "$tunnelLog.err" -ErrorAction SilentlyContinue) -join "`n"
      if ($logText -match "https://[a-z0-9-]+\.trycloudflare\.com") {
        $webhookURL = $Matches[0]
        break
      }
      if ($tunnelProcess.HasExited) { break }
    }
    if ($webhookURL) {
      Write-Host ""
      Write-Host "== Razorpay webhook ready ==" -ForegroundColor Green
      Write-Host "  Endpoint : $webhookURL/api/razorpay/webhook"
      Write-Host "  Register in Razorpay Dashboard -> Settings -> Webhooks:"
      Write-Host "    - Active URL   : $webhookURL/api/razorpay/webhook"
      Write-Host "    - Secret       : value of RAZORPAY_WEBHOOK_SECRET from .env"
      Write-Host "    - Event        : payment.captured"
      Write-Host ""
    } else {
      Write-Host "[tunnel] cloudflared did not report a URL; webhook stays offline this run" -ForegroundColor Yellow
    }
  } else {
    Write-Host "[tunnel] cloudflared not found on PATH; webhook stays offline this run" -ForegroundColor Yellow
  }

  Push-Location web
  try { npm run dev } finally { Pop-Location }
} finally {
  for ($i = $processes.Count - 1; $i -ge 0; $i--) {
    if (-not $processes[$i].HasExited) { Stop-Process -Id $processes[$i].Id -Force }
  }
}
