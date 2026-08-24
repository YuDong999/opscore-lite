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

// SnapshotScript 把 Cmds 的全部采集合并为单条脚本, 在一条 SSH 会话内一次往返完成。
// 每段输出以 __OPSCORE_<KEY>__ 哨兵开头, 由 ParseSections 解析; 键与 Cmds 保持一致,
// 上层(resources/overview)解析逻辑无需改动。
//
// CPU% 改用 /proc/stat 双次采样(间隔约200ms): 比 top -bn1 更快(top 首轮自带~1s),
// 且得到的是真实瞬时占用而非开机以来平均值。所有命令自带兜底, 脚本整体不会非零退出。
const SnapshotScript = `echo __OPSCORE_Hostname__
hostname 2>/dev/null
echo __OPSCORE_OsRelease__
cat /etc/os-release 2>/dev/null | grep -E '^NAME=|^VERSION=' | awk -F= '{gsub(/"/,"",$2); print $1"="$2}'
echo __OPSCORE_Uptime__
cat /proc/uptime 2>/dev/null | awk '{printf "%.0f",$1}'
echo __OPSCORE_CpuCores__
nproc 2>/dev/null || grep -c processor /proc/cpuinfo 2>/dev/null || echo 1
echo __OPSCORE_CpuModel__
_m=$(cat /proc/cpuinfo 2>/dev/null | grep 'model name' | head -1 | awk -F: '{gsub(/"/,"",$2); sub(/^ /,"",$2); print $2}')
if [ -z "$_m" ]; then _m=$(tr -d '\000' < /proc/device-tree/model 2>/dev/null); fi
echo "$_m"
echo __OPSCORE_LoadAvg__
cat /proc/loadavg 2>/dev/null | awk '{print $1" "$2" "$3}'
echo __OPSCORE_CpuUsage__
_read_stat() { grep -E '^cpu[0-9]* ' /proc/stat 2>/dev/null; }
_a=$(_read_stat); sleep 0.2 2>/dev/null || sleep 1; _b=$(_read_stat)
awk -v a="$_a" -v b="$_b" 'BEGIN{
  n=split(a,A," "); m=split(b,B," ");
  if(n<9||m<9){ printf "0.0"; exit }
  ta=0; tb=0;
  for(i=2;i<=9;i++){ ta+=A[i]; tb+=B[i] }
  d=tb-ta; idle=(B[5]+B[6])-(A[5]+A[6]);
  if(d<=0){ printf "0.0"; exit }
  busy=d-idle; if(busy<0) busy=0;
  printf "%.1f", busy/d*100 }'
echo __OPSCORE_CpuPerCore__
awk -v a="$_a" -v b="$_b" 'BEGIN{
  na=split(a,LA,"\n"); nb=split(b,LB,"\n");
  j=2;
  while(j<=na && j<=nb){
    na2=split(LA[j],A," "); nb2=split(LB[j],B," ");
    if(na2>=9 && nb2>=9){
      ta=0; tb=0;
      for(i=2;i<=9;i++){ ta+=A[i]; tb+=B[i] }
      d=tb-ta; idle=(B[5]+B[6])-(A[5]+A[6]);
      v=0;
      if(d>0){ v=(d-idle)/d*100; if(v<0) v=0 }
      printf "%s%.1f", (j>2?" ":""), v
    }
    j++
  }}'
echo __OPSCORE_MemInfo__
free -b 2>/dev/null | awk 'NR==2{printf "%.0f %.0f %.0f",$2,$3,$4}'
echo __OPSCORE_DiskInfo__
df -B1 --exclude-type=tmpfs --exclude-type=devtmpfs --exclude-type=overlay --output=target,size,used 2>/dev/null | tail -n+2 | grep -vE '^/(var/lib/kubelet|var/lib/containerd|run/containerd)(/|$)'
echo __OPSCORE_NetDev__
cat /proc/net/dev 2>/dev/null | tail -n+3 | awk '{n=split($0,k,":"); if(n<2) next; iface=k[1]; gsub(/^ +| +$/,"",iface); split(k[2],b," "); if(iface!="lo" && b[1]!="" && b[9]!="") print iface" "b[1]" "b[9]}'`

// SectionKeys 与 SnapshotScript 输出的哨兵键一一对应(顺序即脚本执行顺序)。
var SectionKeys = []string{
	"Hostname", "OsRelease", "Uptime", "CpuCores", "CpuModel",
	"LoadAvg", "CpuUsage", "CpuPerCore", "MemInfo", "DiskInfo", "NetDev",
}
