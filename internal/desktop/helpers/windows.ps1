$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'
[Console]::InputEncoding = New-Object System.Text.UTF8Encoding($false)
[Console]::OutputEncoding = New-Object System.Text.UTF8Encoding($false)
Add-Type -AssemblyName System.Windows.Forms
Add-Type -AssemblyName System.Drawing
Add-Type -TypeDefinition @'
using System;
using System.Text;
using System.Runtime.InteropServices;
public static class DesktopNative {
 [DllImport("user32.dll")] public static extern bool SetProcessDPIAware();
 [DllImport("user32.dll")] public static extern IntPtr GetForegroundWindow();
 [DllImport("user32.dll", CharSet=CharSet.Unicode)] public static extern int GetWindowText(IntPtr h, StringBuilder text, int max);
 [DllImport("user32.dll")] public static extern bool SetCursorPos(int x, int y);
 [DllImport("user32.dll")] public static extern bool GetCursorPos(out POINT p);
 [DllImport("user32.dll", SetLastError=true)] public static extern uint SendInput(uint count, INPUT[] inputs, int size);
 [DllImport("user32.dll", SetLastError=true)] public static extern IntPtr OpenInputDesktop(uint flags, bool inherit, uint access);
 [DllImport("user32.dll")] public static extern bool CloseDesktop(IntPtr h);
 [StructLayout(LayoutKind.Sequential)] public struct POINT { public int X,Y; }
 [StructLayout(LayoutKind.Sequential)] public struct MOUSEINPUT {public int dx,dy;public uint data,flags,time; public UIntPtr extra;}
 [StructLayout(LayoutKind.Sequential)] public struct KEYBDINPUT {public ushort vk,scan;public uint flags,time;public UIntPtr extra;}
 [StructLayout(LayoutKind.Explicit)] public struct UNION {[FieldOffset(0)]public MOUSEINPUT mouse;[FieldOffset(0)]public KEYBDINPUT key;}
 [StructLayout(LayoutKind.Sequential)] public struct INPUT {public uint type;public UNION u;}
 static void Send(INPUT[] inputs) {if(SendInput((uint)inputs.Length,inputs,Marshal.SizeOf(typeof(INPUT)))!=inputs.Length)throw new Exception("SendInput incomplete or blocked by UIPI/secure desktop; do not replay");}
 static INPUT Mouse(uint flags,uint data=0){var x=new INPUT();x.type=0;x.u.mouse.flags=flags;x.u.mouse.data=data;return x;}
 static INPUT Key(ushort vk,ushort scan,uint flags){var x=new INPUT();x.type=1;x.u.key.vk=vk;x.u.key.scan=scan;x.u.key.flags=flags|((vk==0x5b||vk==0x5c)?1u:0u);return x;}
 public static void Click(string button){uint d=button=="right"?8u:(button=="middle"?32u:2u);Send(new[]{Mouse(d),Mouse(d*2)});}
 public static void Button(string button,bool down){uint d=button=="right"?8u:(button=="middle"?32u:2u);Send(new[]{Mouse(down?d:d*2)});}
 public static void Scroll(int amount){Send(new[]{Mouse(0x0800,unchecked((uint)(-amount*120)))});}
 public static void Type(string text){foreach(char c in text)Send(new[]{Key(0,c,4),Key(0,c,6)});}
 public static string ClipText(string text,int limit){int i=0,count=0;while(i<text.Length&&count<limit){if(char.IsHighSurrogate(text[i])&&i+1<text.Length&&char.IsLowSurrogate(text[i+1]))i+=2;else i++;count++;}return text.Substring(0,i);}
 public static void Chord(ushort[] modifiers,ushort key){var a=new INPUT[modifiers.Length*2+2];int i=0;foreach(var m in modifiers)a[i++]=Key(m,0,0);uint ext=(key>=0x21&&key<=0x28)||key==0x2e?1u:0u;a[i++]=Key(key,0,ext);a[i++]=Key(key,0,ext|2);for(int j=modifiers.Length-1;j>=0;j--)a[i++]=Key(modifiers[j],0,2);Send(a);}
 public static void Release(){Send(new[]{Mouse(4),Mouse(16),Mouse(64),Key(0x10,0,2),Key(0x11,0,2),Key(0x12,0,2),Key(0x5b,0,2)});}
}
'@
[void][DesktopNative]::SetProcessDPIAware()
$script:UIAAvailable = $false
try {
 Add-Type -AssemblyName UIAutomationClient
 Add-Type -AssemblyName UIAutomationTypes
 $script:UIAAvailable = $true
} catch {} # Screenshots and SendInput remain available without UIA.
function Get-UIAPattern($node, $pattern) {
 try { return $node.GetCurrentPattern($pattern) } catch { return $null }
}
function Get-UIARecord($node, [string]$path, [int]$index) {
 $current = $node.Current
 $frame = $current.BoundingRectangle
 $secure = $current.IsPassword
 $invoke = Get-UIAPattern $node ([Windows.Automation.InvokePattern]::Pattern)
 $toggle = Get-UIAPattern $node ([Windows.Automation.TogglePattern]::Pattern)
 $select = Get-UIAPattern $node ([Windows.Automation.SelectionItemPattern]::Pattern)
 $value = $null
 if (-not $secure) { $value = Get-UIAPattern $node ([Windows.Automation.ValuePattern]::Pattern) }
 $name = ([string]$current.Name).Trim()
 $text = ''
 if ($null -ne $value) { $text = ([string]$value.Current.Value).Trim() }
 # Keep a boundary character beyond the 160-character query limit.
 $name=[DesktopNative]::ClipText($name,161)
 $text=[DesktopNative]::ClipText($text,161)
 return @{index=$index;role=$current.ControlType.ProgrammaticName.Replace('ControlType.','');name=$name;value=$text;
  identity=(@($node.GetRuntimeId()) -join '.');enabled=$current.IsEnabled;focused=$current.HasKeyboardFocus;secure=$secure;
  press=($null -ne $invoke -or $null -ne $toggle -or $null -ne $select);
  settable=($null -ne $value -and -not $value.Current.IsReadOnly -and -not $secure);
  x=[int][Math]::Truncate($frame.X);y=[int][Math]::Truncate($frame.Y);w=[int][Math]::Truncate($frame.Width);h=[int][Math]::Truncate($frame.Height);path=$path}
}
function Get-UIAElements($r, [IntPtr]$handle, $bounds) {
 if (-not $script:UIAAvailable) { throw 'UI Automation unavailable; use screenshot coordinates' }
 $root = [Windows.Automation.AutomationElement]::FromHandle($handle)
 $walker = [Windows.Automation.TreeWalker]::ControlViewWalker
 $maxNodes = 500; $limit = 80
 if ($r.action -eq 'query') { $maxNodes = [int]$r.max_nodes; $limit = [int]$r.limit }
 $queue = New-Object 'System.Collections.Generic.List[object]'
 $queue.Add(@{node=$root;path='';depth=0})
 $seen = New-Object 'System.Collections.Generic.HashSet[string]'
 [void]$seen.Add((@($root.GetRuntimeId()) -join '.'))
 $out = New-Object 'System.Collections.Generic.List[object]'
 $head = 0; $visited = 0; $reason = ''
 $clock = [Diagnostics.Stopwatch]::StartNew()
 while ($head -lt $queue.Count) {
  if ($clock.ElapsedMilliseconds -ge 2000) { $reason='timeout'; break }
  if ($visited -ge $maxNodes) { $reason='max_nodes'; break }
  $entry=$queue[$head]; $head++; $visited++
  try {
   $record = Get-UIARecord $entry.node $entry.path $out.Count
   $matches = $true
   if ($r.action -eq 'query') {
    $role = ([string]$r.role).ToLowerInvariant()
    if ($role.StartsWith('ax')) { $role=$role.Substring(2) }
    if ($role -and $record.role.ToLowerInvariant() -ne $role) { $matches=$false }
    if ($r.text) {
     $textMatch=$false
     foreach ($field in @($record.name,$record.value)) {
      if ($r.match_mode -eq 'exact') { if ([string]::Equals($field,[string]$r.text,[StringComparison]::OrdinalIgnoreCase)) { $textMatch=$true } }
      elseif ($field.IndexOf([string]$r.text,[StringComparison]::OrdinalIgnoreCase) -ge 0) { $textMatch=$true }
     }
     if (-not $textMatch) { $matches=$false }
    }
   }
   $visible = -not $entry.node.Current.IsOffscreen -and $record.w -gt 0 -and $record.h -gt 0 -and
    $record.x -lt $bounds.Right -and $record.y -lt $bounds.Bottom -and ($record.x+$record.w) -gt $bounds.Left -and ($record.y+$record.h) -gt $bounds.Top
   if ($entry.path -ne '' -and $matches -and $visible -and ($record.name -or $record.value -or $record.press -or $record.settable)) {
    $out.Add($record)
    if ($out.Count -ge $limit) { $reason='limit'; break }
   }
   $child = $walker.GetFirstChild($entry.node); $i=0
   if ($entry.depth -ge 16) { if ($null -ne $child -and -not $reason) { $reason='max_depth' }; continue }
   while ($null -ne $child) {
    if ($clock.ElapsedMilliseconds -ge 2000) { $reason='timeout'; break }
    if ($queue.Count -ge $maxNodes) { if (-not $reason) { $reason='max_nodes' }; break }
    if ($seen.Add((@($child.GetRuntimeId()) -join '.'))) {
     $path=[string]$i; if ($entry.path -ne '') { $path=$entry.path+'/'+$i }
     $queue.Add(@{node=$child;path=$path;depth=($entry.depth+1)})
    }
    $child=$walker.GetNextSibling($child); $i++
   }
  } catch { if (-not $reason) { $reason='uia_error' } }
 }
 return @{elements=@($out.ToArray());visited_nodes=$visited;truncated=($reason -ne '');stop_reason=$reason}
}
function Resolve-UIATarget($r, [IntPtr]$handle) {
 if (-not $script:UIAAvailable) { throw 'UI Automation unavailable' }
 $node=[Windows.Automation.AutomationElement]::FromHandle($handle)
 $walker=[Windows.Automation.TreeWalker]::ControlViewWalker
 foreach ($part in ([string]$r.element_path).Split('/')) {
  $index=0
  if (-not [int]::TryParse($part,[ref]$index) -or $index -lt 0 -or $index -gt 2000) { throw 'Invalid UIA target path' }
  $node=$walker.GetFirstChild($node)
  for ($i=0;$i -lt $index -and $null -ne $node;$i++) { $node=$walker.GetNextSibling($node) }
  if ($null -eq $node) { throw 'Element changed; observe/query again' }
 }
 $record=Get-UIARecord $node ([string]$r.element_path) 0
 if ($record.role -ne $r.expect_role -or $record.name -cne $r.expect_name -or $record.identity -cne $r.expect_identity -or $record.value -cne [string]$r.expect_value) { throw 'Element identity/value changed; observe/query again' }
 if (-not $record.enabled -or $record.secure) { throw 'Element disabled or authentication requires user' }
 $actual=@($record.x,$record.y,$record.w,$record.h)
 for ($i=0;$i -lt 4;$i++) { if ($actual[$i] -ne $r.expect_frame[$i]) { throw 'Element geometry changed; observe/query again' } }
 if ([DesktopNative]::GetForegroundWindow() -ne $handle) { throw 'Foreground changed; observe/query again' }
 return $node
}
function Assert-Cursor([int]$x,[int]$y) {
 if (-not [DesktopNative]::SetCursorPos($x,$y)) {
  throw 'SetCursorPos failed. The foreground window is likely running at a higher integrity level (UIPI), so this process cannot move the cursor or click it. Do not write a replacement mouse or keyboard driver. Ask the user before running DeepSentry elevated.'
 }
}
# Invoke-Desktop performs one request and returns a hashtable for status (which
# the caller serializes) or $null for actions. Both single-shot and persistent
# serve mode call this, so the injection logic never diverges between the two.
function Invoke-Desktop($r) {
 $bounds = [System.Windows.Forms.Screen]::PrimaryScreen.Bounds
 $handle = [DesktopNative]::GetForegroundWindow()
 $title = New-Object System.Text.StringBuilder 512
 [void][DesktopNative]::GetWindowText($handle,$title,512)
 $inputDesktop = [DesktopNative]::OpenInputDesktop(0,$false,0x0100)
 $ready = [Environment]::UserInteractive -and $handle -ne [IntPtr]::Zero -and $inputDesktop -ne [IntPtr]::Zero
 if ($inputDesktop -ne [IntPtr]::Zero) {[void][DesktopNative]::CloseDesktop($inputDesktop)}
 if ($r.action -eq 'status') {
  return @{driver='windows-sendinput';ready=$ready;reason=$(if($ready){''}else{'No unlocked interactive desktop. Service/session-0, locked RDP and secure UAC desktop are not supported.'});x=$bounds.X;y=$bounds.Y;width=$bounds.Width;height=$bounds.Height;foreground=($handle.ToInt64().ToString()+':'+$title.ToString());window_id=$handle.ToInt64().ToString();surface='primary display'}
 }
 if (-not $ready) {throw 'Interactive desktop unavailable'}
 if ($r.action -ne 'release' -and $r.expect_window_id -and $handle.ToInt64().ToString() -ne $r.expect_window_id) { throw 'Foreground window changed; observe/query again' }
 switch ($r.action) {
  'elements' { return Get-UIAElements $r $handle $bounds }
  'query' { return Get-UIAElements $r $handle $bounds }
  'press_element' {
   $node=Resolve-UIATarget $r $handle
   $pattern=Get-UIAPattern $node ([Windows.Automation.InvokePattern]::Pattern)
   if ($null -ne $pattern) { $pattern.Invoke(); break }
   $pattern=Get-UIAPattern $node ([Windows.Automation.TogglePattern]::Pattern)
   if ($null -ne $pattern) { $pattern.Toggle(); break }
   $pattern=Get-UIAPattern $node ([Windows.Automation.SelectionItemPattern]::Pattern)
   if ($null -ne $pattern) { $pattern.Select(); break }
   throw 'UIA primary action no longer available; observe/query again'
  }
  'click_element' {
   [void](Resolve-UIATarget $r $handle)
   Assert-Cursor ([int]$r.x) ([int]$r.y);[DesktopNative]::Click([string]$r.button)
  }
  'set_value' {
   $node=Resolve-UIATarget $r $handle
   $pattern=Get-UIAPattern $node ([Windows.Automation.ValuePattern]::Pattern)
   if ($null -eq $pattern -or $pattern.Current.IsReadOnly) { throw 'UIA value not settable' }
   $pattern.SetValue([string]$r.text)
  }
  'capture' {
   $bitmap=New-Object System.Drawing.Bitmap $bounds.Width,$bounds.Height
   $graphics=[System.Drawing.Graphics]::FromImage($bitmap)
   try {$graphics.CopyFromScreen($bounds.Location,[System.Drawing.Point]::Empty,$bounds.Size);$bitmap.Save([string]$r.path,[System.Drawing.Imaging.ImageFormat]::Png)}finally{$graphics.Dispose();$bitmap.Dispose()}
  }
  'move' {Assert-Cursor ([int]$r.x) ([int]$r.y)}
  'click' {Assert-Cursor ([int]$r.x) ([int]$r.y);[DesktopNative]::Click([string]$r.button)}
  'double_click' {Assert-Cursor ([int]$r.x) ([int]$r.y);[DesktopNative]::Click([string]$r.button);Start-Sleep -Milliseconds 80;[DesktopNative]::Click([string]$r.button)}
  'drag' {
   Assert-Cursor ([int]$r.x) ([int]$r.y)
   [DesktopNative]::Button([string]$r.button,$true)
   try {
    for($i=1;$i -le 10;$i++){
     Assert-Cursor ([int]($r.x+($r.to_x-$r.x)*$i/10)) ([int]($r.y+($r.to_y-$r.y)*$i/10))
     Start-Sleep -Milliseconds 10
    }
   } finally {[DesktopNative]::Button([string]$r.button,$false)}
  }
  'scroll' {
   if ($r.has_point) { Assert-Cursor ([int]$r.x) ([int]$r.y) }
   [DesktopNative]::Scroll([int]$r.amount)
  }
  'type' {[DesktopNative]::Type([string]$r.text)}
  'key' {
   $parts=([string]$r.key).ToUpperInvariant().Split('+')
   $map=@{ENTER=13;TAB=9;ESCAPE=27;SPACE=32;BACKSPACE=8;DELETE=46;UP=38;DOWN=40;LEFT=37;RIGHT=39;HOME=36;END=35;PAGEUP=33;PAGEDOWN=34;CTRL=17;ALT=18;SHIFT=16;META=91}
   $last=$parts[-1];$key=0
   if($map.ContainsKey($last)){$key=$map[$last]}elseif($last -match '^[A-Z0-9]$'){$key=[int][char]$last}else{throw 'Invalid key'}
   $modifiers=@();for($i=0;$i -lt $parts.Length-1;$i++){if($parts[$i] -notin @('CTRL','ALT','SHIFT','META')){throw 'Invalid modifier'};$modifiers+=$map[$parts[$i]]}
   [DesktopNative]::Chord([uint16[]]$modifiers,[uint16]$key)
  }
  'release' {[DesktopNative]::Release()}
  default {throw 'Unsupported action'}
 }
 return $null
}
# Persistent serve mode: one process, one Add-Type, many newline-delimited
# requests. DeepSentry falls back to a fresh single-shot process if this stream
# ever breaks, so a serve-mode failure never blocks desktop control.
if ($env:DEEPSENTRY_DESKTOP_SERVE) {
 while ($true) {
  $line = [Console]::In.ReadLine()
  if ($null -eq $line) { break }
  $line = $line.Trim()
  if ($line -eq '') { continue }
  try {
   $req = $line | ConvertFrom-Json
   $data = Invoke-Desktop $req
   $json = @{ ok = $true; data = $data } | ConvertTo-Json -Compress -Depth 6
  } catch {
   $json = @{ ok = $false; error = [string]$_.Exception.Message } | ConvertTo-Json -Compress -Depth 6
  }
  [Console]::Out.WriteLine($json)
  [Console]::Out.Flush()
 }
 exit 0
}
try {
 $r = [Console]::In.ReadToEnd() | ConvertFrom-Json
 $data = Invoke-Desktop $r
 if ($null -ne $data) { $data | ConvertTo-Json -Compress }
} catch {
 [Console]::Error.WriteLine([string]$_.Exception.Message)
 exit 1
}
