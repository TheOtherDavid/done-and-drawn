param(
    [switch]$SkipBuild,
    [switch]$ForceRestart
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

$repoRoot = $PSScriptRoot
$envFile = Join-Path $repoRoot '.env'
$buildDir = Join-Path $repoRoot 'build'
$executable = Join-Path $buildDir 'done-and-drawn.exe'
$nextExecutable = Join-Path $buildDir 'done-and-drawn-next.exe'
$allowedNames = @('OPENAI_API_KEY', 'OPENAI_IMAGE_MODEL', 'APP_DATA_DIR', 'APP_ADDR', 'APP_ALLOWED_HOSTS')

if (-not (Test-Path -LiteralPath $envFile)) {
    throw "Missing .env. Copy .env.example to .env and add OPENAI_API_KEY."
}

$settings = @{}
$lineNumber = 0
foreach ($line in [System.IO.File]::ReadLines($envFile)) {
    $lineNumber++
    if ($line.Trim() -eq '' -or $line.TrimStart().StartsWith('#')) { continue }

    $entry = [regex]::Match($line, '^\s*([A-Z][A-Z0-9_]*)\s*=\s*(.*)$')
    if (-not $entry.Success) { throw "Invalid .env entry on line $lineNumber." }

    $name = $entry.Groups[1].Value
    if ($allowedNames -notcontains $name) { throw "Unsupported .env variable '$name' on line $lineNumber." }

    $value = $entry.Groups[2].Value.Trim()
    if ($value.Length -ge 2 -and
        (($value.StartsWith('"') -and $value.EndsWith('"')) -or
         ($value.StartsWith("'") -and $value.EndsWith("'")))) {
        $value = $value.Substring(1, $value.Length - 2)
    }
    $settings[$name] = $value
}

if (-not $settings.ContainsKey('OPENAI_API_KEY') -or [string]::IsNullOrWhiteSpace($settings['OPENAI_API_KEY'])) {
    throw 'Add OPENAI_API_KEY to .env before starting the app.'
}
if (-not $settings.ContainsKey('APP_DATA_DIR') -or $settings['APP_DATA_DIR'] -eq '') {
    $settings['APP_DATA_DIR'] = './data'
}
if (-not $settings.ContainsKey('APP_ADDR') -or $settings['APP_ADDR'] -eq '') {
    $settings['APP_ADDR'] = '127.0.0.1:8080'
}
$addressMatch = [regex]::Match($settings['APP_ADDR'], ':(\d{2,5})$')
if (-not $addressMatch.Success) {
    throw 'APP_ADDR must end with a port, such as 127.0.0.1:8080.'
}
$port = $addressMatch.Groups[1].Value

$dataDir = $settings['APP_DATA_DIR']
if (-not [System.IO.Path]::IsPathRooted($dataDir)) {
    $dataDir = Join-Path $repoRoot $dataDir
}
$dataDir = [System.IO.Path]::GetFullPath($dataDir)
[System.IO.Directory]::CreateDirectory($dataDir) | Out-Null
[System.IO.Directory]::CreateDirectory($buildDir) | Out-Null

$previousEnvironment = @{}
foreach ($name in $allowedNames) {
    $previousEnvironment[$name] = [Environment]::GetEnvironmentVariable($name, 'Process')
}

try {
    # Keep the API key out of the frontend and compiler environments.
    [Environment]::SetEnvironmentVariable('OPENAI_API_KEY', $null, 'Process')

    if (-not $SkipBuild) {
        Push-Location (Join-Path $repoRoot 'frontend')
        try {
            if (-not (Test-Path -LiteralPath (Join-Path $repoRoot 'frontend\node_modules'))) {
                & npm.cmd ci
                if ($LASTEXITCODE -ne 0) { throw 'Frontend dependencies could not be installed.' }
            }
            & npm.cmd run build
            if ($LASTEXITCODE -ne 0) { throw 'Frontend build failed.' }
        } finally {
            Pop-Location
        }

        Push-Location $repoRoot
        try {
            & go build -o $nextExecutable ./cmd/server
            if ($LASTEXITCODE -ne 0) { throw 'Backend build failed.' }
        } finally {
            Pop-Location
        }
    } elseif (-not (Test-Path -LiteralPath $executable)) {
        throw 'No built server found. Run start-local.ps1 without -SkipBuild first.'
    }

    $buildPrefix = $buildDir + [System.IO.Path]::DirectorySeparatorChar
    $knownNames = @('personal-todo', 'personal-todo-fixed', 'personal-todo-mimetype', 'personal-todo-red', 'done-and-drawn')
    foreach ($candidate in @(Get-Process -Name $knownNames -ErrorAction SilentlyContinue)) {
        try { $candidatePath = $candidate.Path } catch { continue }
        if (-not $candidatePath -or -not $candidatePath.StartsWith($buildPrefix, [System.StringComparison]::OrdinalIgnoreCase)) { continue }

        $activeRewards = @()
        $serverResponded = $false
        try {
            $listeners = @(Get-NetTCPConnection -OwningProcess $candidate.Id -State Listen -ErrorAction Stop)
            foreach ($listener in $listeners) {
                $probeHost = $listener.LocalAddress
                if ($probeHost -eq '0.0.0.0') { $probeHost = '127.0.0.1' }
                if ($probeHost -eq '::') { $probeHost = '::1' }
                if ($probeHost.Contains(':') -and -not $probeHost.StartsWith('[')) { $probeHost = "[$probeHost]" }
                try {
                    $tasks = @(Invoke-RestMethod -Uri "http://${probeHost}:$($listener.LocalPort)/api/tasks" -TimeoutSec 5)
                    $activeRewards = @($tasks | Where-Object { $_.reward_status -eq 'queued' -or $_.reward_status -eq 'generating' })
                    $serverResponded = $true
                    break
                } catch { }
            }
            if (-not $serverResponded) { throw 'Could not reach the running server listener.' }
        } catch {
            if (-not $ForceRestart) {
                throw "Cannot confirm whether rewards are generating; the running server was left in place. Retry when it responds, or use -ForceRestart to stop it anyway. A forced stop can lose a paid image result."
            }
            Write-Warning 'Reward status could not be checked. Forcing the restart may lose a paid image result.'
        }
        if ($activeRewards.Count -gt 0) {
            if (-not $ForceRestart) {
                throw "Restart postponed: $($activeRewards.Count) reward(s) are queued or generating. Wait for them to finish, then run this command again. Use -ForceRestart only if you accept that an in-flight image request may be charged again on retry."
            }
            Write-Warning "Forcing restart while $($activeRewards.Count) reward(s) are queued or generating. An in-flight image request may be charged again on retry."
        }
        Write-Host "Stopping previous server (PID $($candidate.Id)) after checking reward status."
        Stop-Process -Id $candidate.Id -Force -ErrorAction SilentlyContinue
        try { $candidate.WaitForExit(10000) | Out-Null } catch { }
        if (-not $candidate.HasExited) { throw "Could not stop the previous server (PID $($candidate.Id))." }
    }

    if (-not $SkipBuild) {
        Copy-Item -LiteralPath $nextExecutable -Destination $executable -Force
        Remove-Item -LiteralPath $nextExecutable -Force
    }

    foreach ($name in $allowedNames) {
        $value = if ($settings.ContainsKey($name) -and $settings[$name] -ne '') { $settings[$name] } else { $null }
        [Environment]::SetEnvironmentVariable($name, $value, 'Process')
    }

    $stdoutLog = Join-Path $dataDir 'server.stdout.log'
    $stderrLog = Join-Path $dataDir 'server.stderr.log'
    $server = Start-Process -FilePath $executable -WorkingDirectory $repoRoot -WindowStyle Hidden -PassThru `
        -RedirectStandardOutput $stdoutLog -RedirectStandardError $stderrLog

    $healthUrl = "http://127.0.0.1:$port/api/health"
    $healthy = $false
    for ($attempt = 0; $attempt -lt 20; $attempt++) {
        Start-Sleep -Milliseconds 350
        try {
            $health = Invoke-RestMethod -Uri $healthUrl -TimeoutSec 2
            if (-not $server.HasExited -and $health.status -eq 'ok') { $healthy = $true; break }
        } catch {
            if ($server.HasExited) { break }
        }
    }
    if (-not $healthy) { throw "Server did not start. Check $stderrLog." }
    Write-Host "Done and Drawn is running at http://127.0.0.1:$port/ (PID $($server.Id))."
} finally {
    foreach ($name in $allowedNames) {
        [Environment]::SetEnvironmentVariable($name, $previousEnvironment[$name], 'Process')
    }
}
