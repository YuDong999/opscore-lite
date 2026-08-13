package remote

var Cmds = map[string]string{
	"CpuUsage":  `top -bn1 2>/dev/null | grep 'Cpu(s)' | awk '{printf "%.1f", $2+$4}'`,
	"MemInfo":   `free -b 2>/dev/null | awk 'NR==2{printf "%.0f %.0f %.0f",$2,$3,$4}'`,
	"DiskInfo":  `df -B1 --exclude-type=tmpfs --exclude-type=devtmpfs --exclude-type=overlay --output=target,size,used 2>/dev/null | tail -n+2 | grep -vE '^/(var/lib/kubelet|var/lib/containerd|run/containerd)(/|$)'`,
	"NetDev":    `cat /proc/net/dev 2>/dev/null | tail -n+3 | awk -F: '{gsub(/ /,"",$1); rx=$2; tx=$3; gsub(/^ +/,"",rx); gsub(/^ +/,"",tx); split(rx,a," "); split(tx,b," "); print $1" "a[1]" "b[1]}'`,
	"Uptime":    `cat /proc/uptime 2>/dev/null | awk '{printf "%.0f",$1}'`,
	"Hostname":  `hostname 2>/dev/null`,
	"OsRelease": `cat /etc/os-release 2>/dev/null | grep -E '^NAME=|^VERSION=' | awk -F= '{gsub(/"/,"",$2); print $1"="$2}'`,
	"LoadAvg":   `cat /proc/loadavg 2>/dev/null | awk '{print $1" "$2" "$3}'`,
	"CpuCores":  `nproc 2>/dev/null || grep -c processor /proc/cpuinfo 2>/dev/null || echo 1`,
	"CpuModel":  `cat /proc/cpuinfo 2>/dev/null | grep 'model name' | head -1 | awk -F: '{gsub(/^ /,"",$2); print $2}'`,
}
