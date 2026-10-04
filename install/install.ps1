# tokenmaxr installer for Windows (PowerShell 5.1+).
#   irm https://github.com/7-of-9/tokenmaxr/releases/latest/download/install.ps1 | iex
# Optional: $env:TOKENMAXR_ARGS, extra install flags, e.g. '--endpoint https://your-server.example'
# or '--no-prompts'.
# Downloads the release listed in latest.json (the same manifest the app's signed
# self-update reads), checks every SHA-256, then runs "tokenmaxr.exe install", which
# copies itself into %LOCALAPPDATA%\tokenmaxr\bin, starts the tray app at login and
# opens its settings page so you can sign in with GitHub.

& {
  $ErrorActionPreference = 'Stop'
  $ProgressPreference = 'SilentlyContinue'
  [Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12

  function Fail($msg) { Write-Host "tokenmaxr: $msg" -ForegroundColor Red }

  $base = if ($env:TOKENMAXR_RELEASE) { $env:TOKENMAXR_RELEASE.TrimEnd('/') } else { 'https://github.com/7-of-9/tokenmaxr/releases/latest/download' }
  # 32-bit and emulated shells report the real CPU in PROCESSOR_ARCHITEW6432 / PROCESSOR_IDENTIFIER.
  $arch = if ($env:PROCESSOR_ARCHITEW6432) { $env:PROCESSOR_ARCHITEW6432 } else { $env:PROCESSOR_ARCHITECTURE }
  if ($env:PROCESSOR_IDENTIFIER -like 'ARM*') { $arch = 'ARM64' }
  switch ($arch) {
    'AMD64' { $key = 'windows-amd64' }
    'ARM64' { $key = 'windows-arm64' }
    default { Fail "unsupported CPU architecture '$arch' (64-bit Windows on x64 or ARM64 only)"; return }
  }

  try { $manifest = Invoke-RestMethod -UseBasicParsing "$base/latest.json" }
  catch { Fail "could not fetch $base/latest.json : $($_.Exception.Message)"; return }
  if ($manifest -is [string]) { $manifest = $manifest | ConvertFrom-Json }
  Write-Host "Installing tokenmaxr $($manifest.version) ($key)"

  $tmp = Join-Path ([IO.Path]::GetTempPath()) ('tokenmaxr-' + [Guid]::NewGuid().ToString('N').Substring(0, 8))
  New-Item -ItemType Directory -Path $tmp | Out-Null
  try {
    # The console binary and its windowless twin (the tray app) side by side,
    # under the names install expects.
    foreach ($k in @($key, "$key-w")) {
      $f = $manifest.files.$k
      if (-not $f) { Fail "latest.json has no '$k' build"; return }
      $name = if ($k.EndsWith('-w')) { 'tokenmaxrw.exe' } else { 'tokenmaxr.exe' }
      $out = Join-Path $tmp $name
      try { Invoke-WebRequest -UseBasicParsing -Uri $f.url -OutFile $out }
      catch { Fail "download failed: $($f.url) : $($_.Exception.Message)"; return }
      if ((Get-FileHash -Algorithm SHA256 $out).Hash -ne $f.sha256) { Fail "SHA-256 mismatch for $($f.url); not installing"; return }
    }

    $exe = Join-Path $tmp 'tokenmaxr.exe'
    $cmdArgs = @('install')
    if ($env:TOKENMAXR_ARGS) { $cmdArgs += ($env:TOKENMAXR_ARGS -split '\s+' | Where-Object { $_ }) }
    try { & $exe @cmdArgs; $code = $LASTEXITCODE }
    catch {
      Fail "could not start tokenmaxr.exe: $($_.Exception.Message)"
      Write-Host 'If Windows blocked it, Smart App Control may be on: Windows Security > App & browser control > Smart App Control.' -ForegroundColor Yellow
      return
    }
    if ($code -ne 0) { Fail "install exited with code $code"; return }
  }
  finally { Remove-Item -Recurse -Force $tmp -ErrorAction SilentlyContinue }
}
