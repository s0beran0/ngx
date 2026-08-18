<#
    The ngx installer for Windows.

        irm https://raw.githubusercontent.com/s0beran0/ngx/main/install.ps1 | iex

    The equivalent of install.sh, with the differences the platform imposes:
    it downloads a .zip instead of a .tar.gz, installs into
    %LOCALAPPDATA%\ngx\bin (writable without elevation, unlike /usr/local/bin
    on Unix) and adds the directory to the user's PATH.

    The order of the steps is deliberate: everything that can fail without the
    network fails BEFORE the first download — architecture, directory, write
    permission and verification tools.

    This file is written to be executed through "irm | iex" as well, so it
    uses neither #Requires (which only applies to a file on disk) nor
    $MyInvocation.MyCommand.Path (which is null in that mode).
#>

param(
    [switch] $Help
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
# The Invoke-WebRequest progress meter costs more than the download itself on
# PowerShell 5.1, and it pollutes the output for whoever is reading the result.
$ProgressPreference = 'SilentlyContinue'

$Repository  = 's0beran0/ngx'
$ApiUrl      = "https://api.github.com/repos/$Repository"
$ReleasesUrl = "https://github.com/$Repository/releases"

# ---------------------------------------------------------------------------
# MINISIGN PUBLIC KEY (DD2/DD3) — real key, already generated.
#
# The placeholder constant below is kept on purpose: it is what the script
# compares against to decide whether verification is possible at all. A fork
# that has not generated its own key still gets a refusal instead of a silent
# install.
# ---------------------------------------------------------------------------
# The project's public key HAS NOT BEEN GENERATED YET (Task D2). The value
# below is a deliberate placeholder and is NOT a key: a real minisign key is a
# single base64 line of 56 characters starting with "RW". The text was written
# to be impossible to mistake for a real key — a plausible value would slip
# through review and reach production verifying nothing.
#
# When generating the key, replace the line below with the key line from the
# ngx-minisign.pub file (the second line, without the "untrusted comment:").
#
# While the placeholder is here, the script REFUSES to install: absence of
# verification is a failure, never a "carried on anyway".
$MinisignPublicKey = 'RWSZFXRcIf6p0xLvenNPLgltwYLa/qRAjNH3sA238fWZIy49RGIbtgAW'
$KeyPlaceholder    = 'PLACEHOLDER-CHAVE-MINISIGN-NAO-GERADA-VER-TASK-D2'

function Show-Help {
    @'
install.ps1 - the ngx installer for Windows

USAGE
  irm https://raw.githubusercontent.com/s0beran0/ngx/main/install.ps1 | iex
  .\install.ps1 [-Help]

ENVIRONMENT VARIABLES
  NGX_INSTALL_DIR       Installation directory.
                        Default: %LOCALAPPDATA%\ngx\bin
                        A directory such as C:\Program Files requires
                        PowerShell as administrator; the script detects that
                        before downloading and says what to do, without trying
                        to elevate on its own.
  NGX_CHANNEL           stable (default) or beta. beta includes pre-releases.
  NGX_VERSION           Pinned version, e.g. v0.2.0. When set, the GitHub API
                        is not queried.
  NGX_ALLOW_UNVERIFIED  If 1, allows installing when the minisign signature
                        CANNOT be verified (minisign missing or public key
                        not generated yet). It does NOT ignore an invalid
                        signature or a mismatched checksum: those always
                        abort, no exceptions.

EXAMPLES
  $env:NGX_VERSION='v0.2.0'; irm https://raw.githubusercontent.com/s0beran0/ngx/main/install.ps1 | iex
  $env:NGX_INSTALL_DIR='D:\tools\bin'; .\install.ps1

VERIFICATION
  The SHA256 checksum is always checked and there is no way to turn it off.
  The minisign signature of checksums.txt is checked when minisign is
  installed and the project's public key is embedded in this script.
'@ | Write-Host
}

function Write-Line {
    param([string] $Text = '')
    # Write-Host is deliberate: this script's output is for a person reading
    # the terminal, and the pipeline should not carry diagnostic text.
    Write-Host $Text
}

# Aborts with "throw", not with "exit". In the documented flow — irm | iex —
# the script runs in the scope of the interactive session, and there "exit"
# closes PowerShell itself: the window closes and the person never reads the
# message that was just printed. throw stops execution, keeps the session
# alive and, when the script is called from a file (powershell -File), still
# produces a non-zero exit code for automation.
function Fail {
    param(
        [string]   $Message,
        [string[]] $Details = @()
    )
    Write-Host "error: $Message" -ForegroundColor Red
    foreach ($entry in $Details) { Write-Host $entry }
    throw "installation aborted: $Message"
}

if ($Help) {
    Show-Help
    return
}

if ($PSVersionTable.PSVersion.Major -lt 5) {
    Fail "this script requires PowerShell 5.1 or newer (found $($PSVersionTable.PSVersion))" @(
        '',
        'Windows 10 and Windows Server 2016 already ship version 5.1.',
        'on earlier versions, install Windows Management Framework 5.1 or',
        'PowerShell 7: https://aka.ms/powershell'
    )
}

# PowerShell 5.1 negotiates TLS 1.0 by default on some installations, and
# GitHub refuses it. On PowerShell 7 the default is already fine and touching
# this is unnecessary.
if ($PSVersionTable.PSVersion.Major -lt 6) {
    try {
        [Net.ServicePointManager]::SecurityProtocol =
            [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12
    } catch {
        # If the platform does not expose Tls12, the download fails later with
        # a message of its own; there is nothing to do here.
    }
}

# ---------------------------------------------------------------------------
# Step 1 - architecture
# ---------------------------------------------------------------------------

function Get-Architecture {
    # PROCESSOR_ARCHITECTURE reports x86 when a 32-bit PowerShell runs on a
    # 64-bit Windows; in that case the real architecture is in
    # PROCESSOR_ARCHITEW6432. Ignoring this would install the wrong binary.
    $architecture = $env:PROCESSOR_ARCHITEW6432
    if ([string]::IsNullOrEmpty($architecture)) {
        $architecture = $env:PROCESSOR_ARCHITECTURE
    }

    switch ($architecture) {
        'AMD64' { return 'amd64' }
        'ARM64' { return 'arm64' }
        default {
            Fail "unsupported architecture: $architecture" @(
                '',
                'ngx publishes binaries for amd64 (x64) and arm64.',
                'for other architectures, build from source:',
                "  git clone https://github.com/$Repository.git; cd ngx; go build ./cmd/ngx"
            )
        }
    }
}

# ---------------------------------------------------------------------------
# Step 2 - installation directory and permission
# ---------------------------------------------------------------------------

function Fail-Privilege {
    param([string] $Reason, [string] $Directory)

    Fail $Reason @(
        '',
        'open PowerShell as administrator and run the installation again:',
        '  right-click PowerShell > "Run as administrator"',
        '',
        'or leave NGX_INSTALL_DIR unset: the default is %LOCALAPPDATA%\ngx\bin,',
        'which is writable without elevation. to clear the variable in this session:',
        "  Remove-Item Env:NGX_INSTALL_DIR",
        '',
        "the requested directory was: $Directory",
        '',
        'this script does not try to elevate privilege on its own: you are the',
        'one who decides to elevate, with the command in front of you.'
    )
}

function Prepare-Directory {
    $directory = $env:NGX_INSTALL_DIR

    if ([string]::IsNullOrEmpty($directory)) {
        if ([string]::IsNullOrEmpty($env:LOCALAPPDATA)) {
            Fail 'LOCALAPPDATA is not set and neither is NGX_INSTALL_DIR' @(
                '',
                'point at the directory explicitly:',
                "  `$env:NGX_INSTALL_DIR='C:\ngx\bin'"
            )
        }
        $directory = Join-Path $env:LOCALAPPDATA 'ngx\bin'
    }

    if (Test-Path -LiteralPath $directory -PathType Leaf) {
        Fail "$directory exists and is not a directory"
    }

    if (-not (Test-Path -LiteralPath $directory)) {
        try {
            New-Item -ItemType Directory -Path $directory -Force | Out-Null
        } catch {
            Fail-Privilege "could not create the directory $directory" $directory
        }
    }

    # Actually writing is the only test that does not lie: Test-Path says
    # nothing about permission, and the effective ACL of a protected directory
    # only shows up at write time.
    $testFile = Join-Path $directory ".ngx-write-test-$PID"
    try {
        [System.IO.File]::WriteAllText($testFile, 'ngx')
        Remove-Item -LiteralPath $testFile -Force
    } catch {
        Fail-Privilege "no write permission in $directory" $directory
    }

    return $directory
}

# ---------------------------------------------------------------------------
# Step 3 - verification (before downloading)
# ---------------------------------------------------------------------------

function Test-CommandExists {
    param([string] $Name)
    return [bool] (Get-Command $Name -ErrorAction SilentlyContinue)
}

# Three outcomes, none of them silent: it can be verified; it cannot and there
# is no authorization (abort); it cannot and there is explicit authorization
# (carry on with a warning).
function Assess-SignatureVerification {
    $reason = ''

    if ($MinisignPublicKey -eq $KeyPlaceholder) {
        $reason = "the project's minisign public key has not been generated yet and this script carries a placeholder"
    } elseif (-not (Test-CommandExists 'minisign')) {
        $reason = 'minisign is not installed on this machine'
    }

    if ($reason -eq '') {
        return $true
    }

    if ($env:NGX_ALLOW_UNVERIFIED -eq '1') {
        Write-Line ''
        Write-Host '############################################################' -ForegroundColor Yellow
        Write-Host '# WARNING: INSTALLING WITHOUT VERIFYING THE SIGNATURE'       -ForegroundColor Yellow
        Write-Host '#'                                                          -ForegroundColor Yellow
        Write-Host "# $reason."                                                 -ForegroundColor Yellow
        Write-Host '#'                                                          -ForegroundColor Yellow
        Write-Host '# NGX_ALLOW_UNVERIFIED=1 is set, so the installation'        -ForegroundColor Yellow
        Write-Host '# carries on. The SHA256 checksum will still be checked,'    -ForegroundColor Yellow
        Write-Host '# but it only protects against a corrupted download: it'     -ForegroundColor Yellow
        Write-Host '# does not protect against a release published by whoever'   -ForegroundColor Yellow
        Write-Host '# compromised the GitHub account, because in that case the'  -ForegroundColor Yellow
        Write-Host '# checksum would come tampered with as well.'                -ForegroundColor Yellow
        Write-Host '############################################################' -ForegroundColor Yellow
        Write-Line ''
        return $false
    }

    $details = @(
        '',
        "reason: $reason.",
        '',
        'ngx operates the configuration of a server that serves traffic.',
        'installing a binary without verifying where it came from is not a',
        'hygiene detail. that is why the script stops here instead of carrying',
        'on.',
        '',
        'how to fix it:'
    )

    if ($MinisignPublicKey -eq $KeyPlaceholder) {
        $details += @(
            '  the public key does not exist yet - there is nothing you can do',
            "  on your side. follow $ReleasesUrl and use a version of this",
            '  script published after the first signed release.'
        )
    } else {
        $details += @(
            '  install minisign and run again:',
            '    winget install jedisct1.minisign',
            '    or download it from https://github.com/jedisct1/minisign/releases'
        )
    }

    $details += @(
        '',
        'if you accept the risk knowingly, and only in that case:',
        "  `$env:NGX_ALLOW_UNVERIFIED='1'"
    )

    Fail 'the release signature could not be verified' $details
}

# ---------------------------------------------------------------------------
# Step 4 - network
# ---------------------------------------------------------------------------

# The exception type changes between PowerShell 5.1 (WebException) and 7
# (HttpRequestException), and the HTTP code lives in different places. This
# function returns 0 when it could not be determined.
function Get-HttpCode {
    param($ErrorRecord)

    try {
        $response = $ErrorRecord.Exception.Response
        if ($null -eq $response) { return 0 }

        $status = $response.StatusCode
        if ($null -eq $status) { return 0 }

        return [int] $status
    } catch {
        return 0
    }
}

function Fail-Release {
    param([int] $Code, [string] $Where, [string] $Version = '')

    switch ($Code) {
        404 {
            $details = @(
                '',
                'the two possible causes:',
                '  1. the project has not published any release yet. check at',
                "     $ReleasesUrl"
            )
            if ($Version -ne '') {
                $details += @(
                    "  2. the requested version, $Version, does not exist. the tag",
                    '     name includes the leading "v": v0.1.0, not 0.1.0.'
                )
            } else {
                $details += @(
                    '  2. only pre-releases exist. try the beta channel:',
                    "     `$env:NGX_CHANNEL='beta'"
                )
            }
            Fail "no release found for $Repository ($Where answered 404)" $details
        }
        403 {
            Fail "the GitHub API refused the query (HTTP 403) - likely a per-IP rate limit" @(
                '',
                'the anonymous limit is per hour and per address. two ways out:',
                '  - wait and try again, or',
                '  - pin the version, which skips the API query:',
                "      `$env:NGX_VERSION='v0.1.0'"
            )
        }
        429 {
            Fail "the GitHub API refused the query (HTTP 429) - rate limit" @(
                '',
                'wait a few minutes, or pin the version to skip the API:',
                "  `$env:NGX_VERSION='v0.1.0'"
            )
        }
        0 {
            Fail "could not talk to $Where" @(
                '',
                'check the network connection, DNS and whether a proxy requires',
                'configuration. no file was written.'
            )
        }
        default {
            Fail "unexpected response from ${Where}: HTTP $Code" @(
                '',
                'check the service status at https://www.githubstatus.com'
            )
        }
    }
}

function Download-File {
    param([string] $Url, [string] $Destination)

    try {
        Invoke-WebRequest -Uri $Url -OutFile $Destination -UseBasicParsing -ErrorAction Stop
        return 200
    } catch {
        return (Get-HttpCode $_)
    }
}

# Set-StrictMode turns access to a nonexistent property into a runtime error.
# An API response outside the expected shape would blow up with a PowerShell
# stack trace instead of this script's useful message.
function Get-TextProperty {
    param($Object, [string] $Name)

    if ($null -eq $Object) { return '' }
    $property = $Object.PSObject.Properties[$Name]
    if ($null -eq $property -or $null -eq $property.Value) { return '' }
    return [string] $property.Value
}

function Resolve-Version {
    if (-not [string]::IsNullOrEmpty($env:NGX_VERSION)) {
        return $env:NGX_VERSION
    }

    $channel = $env:NGX_CHANNEL
    if ([string]::IsNullOrEmpty($channel)) { $channel = 'stable' }

    switch ($channel) {
        'stable' { $url = "$ApiUrl/releases/latest" }
        'beta'   { $url = "$ApiUrl/releases?per_page=1" }
        default  {
            Fail "unknown channel: $channel" @(
                '',
                "the accepted values are 'stable' (default) and 'beta'."
            )
        }
    }

    try {
        $response = Invoke-RestMethod -Uri $url -UseBasicParsing -ErrorAction Stop
    } catch {
        Fail-Release (Get-HttpCode $_) 'the GitHub API'
    }

    # The beta channel returns a list; @() normalizes the single-element case,
    # which PowerShell 5.1 hands over as a bare object.
    if ($channel -eq 'beta') {
        $list = @($response)
        if ($list.Count -eq 0) {
            Fail "the GitHub API answered, but no release was found in the beta channel" @(
                '',
                'the beta channel lists every release, pre-releases included,',
                'and the list came back empty: the project has not published any.',
                "check at $ReleasesUrl"
            )
        }
        $response = $list[0]
    }

    $tag = Get-TextProperty $response 'tag_name'
    if ([string]::IsNullOrEmpty($tag)) {
        Fail "the GitHub API answered, but no release was found in the $channel channel" @(
            '',
            "check at $ReleasesUrl. if the project has only published",
            "pre-releases so far, use: `$env:NGX_CHANNEL='beta'"
        )
    }

    return $tag
}

# ---------------------------------------------------------------------------
# Flow
# ---------------------------------------------------------------------------

$architecture = Get-Architecture
$directory    = Prepare-Directory

if (-not (Test-CommandExists 'Expand-Archive')) {
    Fail 'the Expand-Archive cmdlet is not available' @(
        '',
        'it ships with PowerShell 5.0 and newer. update PowerShell:',
        '  https://aka.ms/powershell'
    )
}

$verifySignature = Assess-SignatureVerification
$version         = Resolve-Version

$tempDir = Join-Path ([System.IO.Path]::GetTempPath()) ("ngx-install-" + [System.Guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $tempDir -Force | Out-Null

try {
    # goreleaser derives the file name from the version without the leading "v".
    $versionNoV   = $version -replace '^v', ''
    $fileName     = "ngx_${versionNoV}_windows_${architecture}.zip"
    $downloadBase = "$ReleasesUrl/download/$version"

    $zipPath       = Join-Path $tempDir $fileName
    $checksumsPath = Join-Path $tempDir 'checksums.txt'
    $signaturePath = Join-Path $tempDir 'checksums.txt.minisig'

    Write-Line "downloading ngx $version for windows/$architecture..."

    $code = Download-File "$downloadBase/$fileName" $zipPath
    if ($code -ne 200) {
        if ($code -eq 404) {
            # GitHub answers 404 both for a nonexistent tag and for a missing
            # file in a release that does exist. There is no way to tell them
            # apart by the code, so the message covers both instead of
            # asserting something that was not verified.
            Fail "could not download $fileName from release $version (HTTP 404)" @(
                '',
                'the two possible causes:',
                "  1. release $version does not exist. the tag name includes the",
                "     leading 'v': v0.1.0, not 0.1.0.",
                "  2. the release exists but does not publish the artifact for windows/$architecture.",
                '',
                'check what exists at:',
                "  $ReleasesUrl/tag/$version"
            )
        }
        Fail-Release $code 'the release download' $version
    }

    $code = Download-File "$downloadBase/checksums.txt" $checksumsPath
    if ($code -ne 200) {
        Fail "release $version does not publish checksums.txt (HTTP $code)" @(
            '',
            'without the checksum there is no way to check the download, and',
            'installing without checking is not an option. check the release at:',
            "  $ReleasesUrl/tag/$version"
        )
    }

    if ($verifySignature) {
        $code = Download-File "$downloadBase/checksums.txt.minisig" $signaturePath
        if ($code -ne 200) {
            Fail "release $version does not publish checksums.txt.minisig (HTTP $code)" @(
                '',
                'the public key is in this script, so the signature was',
                'expected. a signed release that loses its signature is a sign',
                'of trouble in the publishing process - not of something to',
                'work around.',
                '',
                "check the release at $ReleasesUrl/tag/$version"
            )
        }

        # The minisign exit code is what matters, and it lives in
        # $LASTEXITCODE because minisign is an external executable, not a
        # cmdlet.
        #
        # ErrorActionPreference goes back to Continue during the call: with
        # 'Stop', any line an external executable writes to stderr becomes a
        # NativeCommandError and aborts the script with a PowerShell message,
        # swallowing ours. Here the exit code is what decides.
        $previousPreference = $ErrorActionPreference
        $ErrorActionPreference = 'Continue'
        & minisign -V -q -m $checksumsPath -x $signaturePath -P $MinisignPublicKey | Out-Null
        $minisignCode = $LASTEXITCODE
        $ErrorActionPreference = $previousPreference

        if ($minisignCode -ne 0) {
            Fail 'the minisign signature of checksums.txt does NOT check out' @(
                '',
                'the downloaded file was not signed by the project key. this is',
                'not a network error: it is an artifact that should not exist.',
                '',
                'nothing was installed. do not work around this error.'
            )
        }

        Write-Line 'minisign signature checked.'
    }

    # goreleaser's checksums.txt has one "<sha256>  <file>" line per artifact,
    # in the sha256sum format: two spaces between hash and name.
    $expected = ''
    foreach ($entry in (Get-Content -LiteralPath $checksumsPath)) {
        $parts = $entry -split '\s+', 2
        if ($parts.Count -eq 2 -and $parts[1].Trim() -eq $fileName) {
            $expected = $parts[0].Trim()
            break
        }
    }

    if ($expected -eq '') {
        Fail "checksums.txt does not list $fileName" @(
            '',
            "the checksum file of release $version does not cover the",
            'downloaded artifact. nothing was installed.'
        )
    }

    $got = (Get-FileHash -LiteralPath $zipPath -Algorithm SHA256).Hash

    # -ine: Get-FileHash returns uppercase and sha256sum lowercase.
    if ($expected -ine $got) {
        Fail "the SHA256 of $fileName does not match" @(
            '',
            "  expected: $expected",
            "  got:      $got",
            '',
            'the download came corrupted or was altered along the way. nothing',
            'was installed. try again; if it persists, do not install this file.'
        )
    }

    Write-Line 'SHA256 checksum verified.'

    $extractedDir = Join-Path $tempDir 'extracted'
    Expand-Archive -LiteralPath $zipPath -DestinationPath $extractedDir -Force

    $source = Join-Path $extractedDir 'ngx.exe'
    if (-not (Test-Path -LiteralPath $source -PathType Leaf)) {
        Fail "the ngx.exe binary was not found inside $fileName"
    }

    # Copy to the final destination and only then rename: that way there is
    # never a moment when ngx.exe is half written in the installation
    # directory.
    $destination        = Join-Path $directory 'ngx.exe'
    $partialDestination = Join-Path $directory ".ngx.new.$PID.exe"
    try {
        Copy-Item -LiteralPath $source -Destination $partialDestination -Force
        Move-Item -LiteralPath $partialDestination -Destination $destination -Force
    } catch {
        if (Test-Path -LiteralPath $partialDestination) {
            Remove-Item -LiteralPath $partialDestination -Force -ErrorAction SilentlyContinue
        }
        Fail "could not write $destination" @(
            '',
            'if ngx is running, Windows locks the file. close the process and',
            'run again.',
            '',
            "detail: $($_.Exception.Message)"
        )
    }

    Write-Line "ngx $version installed at $destination"

    # The user's PATH: User scope, which does not require elevation.
    #
    # Reading and writing go through the registry instead of
    # [Environment]::GetEnvironmentVariable/SetEnvironmentVariable because
    # that API expands the variables embedded in the value. A PATH containing
    # "%USERPROFILE%\bin" comes back from the API already expanded into the
    # literal path, and writing it back destroys the reference — the person
    # loses the portability of their own PATH because of one installation.
    # Reading with DoNotExpandEnvironmentNames and writing back with the same
    # kind (ExpandString) preserves the original value.
    $environmentKey = 'HKCU:\Environment'
    $userPath       = ''
    $valueKind      = [Microsoft.Win32.RegistryValueKind]::ExpandString
    $readOk         = $false

    try {
        $key   = Get-Item -LiteralPath $environmentKey
        $value = $key.GetValue(
            'Path', '', [Microsoft.Win32.RegistryValueOptions]::DoNotExpandEnvironmentNames)
        if ($null -ne $value) { $userPath = [string] $value }
        if ($userPath -ne '') { $valueKind = $key.GetValueKind('Path') }
        $readOk = $true
    } catch {
        $readOk = $false
    }

    $alreadyInPath = $false
    foreach ($part in ($userPath -split ';')) {
        if ($part.Trim() -ne '' -and $part.Trim().TrimEnd('\') -ieq $directory.TrimEnd('\')) {
            $alreadyInPath = $true
            break
        }
    }

    if ($alreadyInPath) {
        Write-Line "run 'ngx version' to check."
    } elseif (-not $readOk) {
        Write-Line ''
        Write-Line "warning: your user PATH could not be read, so it was not"
        Write-Line 'changed. add the directory manually:'
        Write-Line "  $directory"
    } else {
        try {
            $newPath = if ($userPath.Trim() -eq '') {
                $directory
            } else {
                "$($userPath.TrimEnd(';'));$directory"
            }
            Set-ItemProperty -LiteralPath $environmentKey -Name 'Path' -Value $newPath -Type $valueKind
            Write-Line ''
            Write-Line "$directory was added to your user PATH."
            Write-Line 'open a new terminal for the change to take effect - the current'
            Write-Line 'window keeps the old PATH.'
        } catch {
            Write-Line ''
            Write-Line "warning: could not change the user PATH ($($_.Exception.Message))."
            Write-Line 'ngx was installed; only the PATH was left undone. add:'
            Write-Line "  $directory"
        }
    }

    Write-Line ''
    Write-Line 'a note about Windows: nginx for Windows is distributed as a beta build'
    Write-Line 'by nginx.org itself and is not installed by a package manager.'
    Write-Line 'point ngx at the unpacked directory with -c, for example:'
    Write-Line '  ngx -c C:\nginx-1.31.3\conf\nginx.conf inspect'
}
finally {
    if (Test-Path -LiteralPath $tempDir) {
        Remove-Item -LiteralPath $tempDir -Recurse -Force -ErrorAction SilentlyContinue
    }
}
