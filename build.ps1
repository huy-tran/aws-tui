# Windows-native build helper. PowerShell equivalent of `make build`.
# Usage:
#   .\build.ps1            # local build to bin\aws-tui.exe
#   .\build.ps1 -Install   # go install into $GOBIN so `aws-tui` resolves on PATH
#   .\build.ps1 -Dist      # cross-platform builds into dist\

param(
    [switch]$Dist,
    [switch]$Install
)

$ErrorActionPreference = "Stop"

try { $Version = (& git describe --tags --always --dirty 2>$null) } catch { $Version = "dev" }
if (-not $Version) { $Version = "dev" }
try { $Commit = (& git rev-parse --short HEAD 2>$null) } catch { $Commit = "none" }
if (-not $Commit) { $Commit = "none" }
$Date = (Get-Date).ToUniversalTime().ToString("yyyy-MM-ddTHH:mm:ssZ")

$Ldflags = "-s -w -X main.version=$Version -X main.commit=$Commit -X main.date=$Date"

if ($Install) {
    Write-Host "installing aws-tui (version $Version) into `$GOBIN..."
    & go install -ldflags "$Ldflags" .\cmd\aws-tui
    if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
    Write-Host "done. run 'aws-tui --version' to confirm."
    exit 0
}

if ($Dist) {
    New-Item -ItemType Directory -Force -Path dist | Out-Null
    $targets = @(
        @{ GOOS = "windows"; GOARCH = "amd64"; Out = "dist\aws-tui-windows-amd64.exe" },
        @{ GOOS = "darwin";  GOARCH = "amd64"; Out = "dist\aws-tui-darwin-amd64"      },
        @{ GOOS = "darwin";  GOARCH = "arm64"; Out = "dist\aws-tui-darwin-arm64"      },
        @{ GOOS = "linux";   GOARCH = "amd64"; Out = "dist\aws-tui-linux-amd64"       },
        @{ GOOS = "linux";   GOARCH = "arm64"; Out = "dist\aws-tui-linux-arm64"       }
    )
    foreach ($t in $targets) {
        $env:GOOS = $t.GOOS
        $env:GOARCH = $t.GOARCH
        Write-Host "building $($t.Out)..."
        go build -ldflags "$Ldflags" -o $t.Out .\cmd\aws-tui
    }
    Remove-Item Env:GOOS, Env:GOARCH -ErrorAction SilentlyContinue
    Write-Host "done"
} else {
    New-Item -ItemType Directory -Force -Path bin | Out-Null
    Write-Host "building bin\aws-tui.exe (version $Version)..."
    go build -ldflags "$Ldflags" -o bin\aws-tui.exe .\cmd\aws-tui
    Write-Host "done"
}
