package ansible

import (
	"fmt"
	"sort"
)

type Template struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Category    string `json:"category"`
	Content     string `json:"content"`
}

func BuiltinTemplates() []Template {
	return []Template{
		{
			ID: "init-system", Name: "系统初始化", Category: "系统",
			Description: "设置主机名、时区、NTP、limits、sysctl、EPEL 等基础环境",
			Content: `---
- name: 系统初始化
  hosts: all
  gather_facts: yes
  vars:
    hostname_prefix: "node"
    timezone: "Asia/Shanghai"
    ntp_servers:
      - ntp.aliyun.com
      - ntp.tencent.com
  tasks:
    - name: 设置主机名
      hostname:
        name: "{{ hostname_prefix }}-{{ inventory_hostname_short }}"

    - name: 设置时区
      timezone:
        zone: "{{ timezone }}"

    - name: 安装 chrony
      yum:
        name: chrony
        state: present

    - name: 配置 NTP 服务器
      lineinfile:
        path: /etc/chrony.conf
        regexp: "^pool"
        line: "pool {{ item }} iburst"
        state: present
      loop: "{{ ntp_servers }}"

    - name: 启动 chronyd
      systemd:
        name: chronyd
        enabled: yes
        state: started

    - name: 设置系统 limits
      copy:
        dest: /etc/security/limits.d/90-custom.conf
        content: |
          * soft nofile 655360
          * hard nofile 655360
          * soft nproc 655360
          * hard nproc 655360

    - name: 设置 sysctl 参数
      sysctl:
        name: "{{ item.key }}"
        value: "{{ item.value }}"
        sysctl_set: yes
        state: present
        reload: yes
      loop:
        - { key: net.ipv4.ip_forward, value: "1" }
        - { key: net.core.somaxconn, value: "65535" }
        - { key: vm.swappiness, value: "10" }

    - name: 安装 EPEL
      yum:
        name: epel-release
        state: present`,
		},
		{
			ID: "install-docker", Name: "安装 Docker CE", Category: "容器",
			Description: "安装 Docker CE、docker-compose、配置镜像加速和日志轮转",
			Content: `---
- name: 安装 Docker CE
  hosts: all
  gather_facts: yes
  vars:
    docker_version: "latest"
    docker_mirror: "https://docker.mirrors.ustc.edu.cn"
    log_max_size: "100m"
    log_max_file: "3"
  tasks:
    - name: 安装依赖
      yum:
        name:
          - yum-utils
          - device-mapper-persistent-data
          - lvm2
        state: present

    - name: 添加 Docker YUM 源
      get_url:
        url: https://download.docker.com/linux/centos/docker-ce.repo
        dest: /etc/yum.repos.d/docker-ce.repo

    - name: 安装 Docker CE
      yum:
        name: "{{ 'docker-ce' if docker_version == 'latest' else 'docker-ce-' + docker_version }}"
        state: present

    - name: 配置 daemon.json
      copy:
        dest: /etc/docker/daemon.json
        content: |
          {
            "registry-mirrors": ["{{ docker_mirror }}"],
            "log-driver": "json-file",
            "log-opts": {
              "max-size": "{{ log_max_size }}",
              "max-file": "{{ log_max_file }}"
            },
            "storage-driver": "overlay2"
          }

    - name: 启动 dockerd
      systemd:
        name: docker
        enabled: yes
        state: started

    - name: 安装 docker-compose
      get_url:
        url: https://github.com/docker/compose/releases/latest/download/docker-compose-linux-x86_64
        dest: /usr/local/bin/docker-compose
        mode: "0755"`,
		},
		{
			ID: "setup-yum", Name: "YUM 源配置", Category: "系统",
			Description: "配置阿里云/163 镜像源、EPEL、清理缓存",
			Content: `---
- name: 配置 YUM 源
  hosts: all
  gather_facts: yes
  vars:
    base_repo: "http://mirrors.aliyun.com/repo/Centos-7.repo"
    epel_repo: "http://mirrors.aliyun.com/repo/epel-7.repo"
  tasks:
    - name: 备份原始 repo
      shell: |
        mkdir -p /etc/yum.repos.d/backup
        mv /etc/yum.repos.d/*.repo /etc/yum.repos.d/backup/ 2>/dev/null || true

    - name: 下载 Base 源
      get_url:
        url: "{{ base_repo }}"
        dest: /etc/yum.repos.d/CentOS-Base.repo

    - name: 下载 EPEL 源
      get_url:
        url: "{{ epel_repo }}"
        dest: /etc/yum.repos.d/epel.repo

    - name: 清理缓存
      command: yum clean all

    - name: 重建缓存
      command: yum makecache`,
		},
		{
			ID: "deploy-monitoring", Name: "部署监控 Agent", Category: "监控",
			Description: "部署 node_exporter、blackbox_exporter，配置 Prometheus 采集目标",
			Content: `---
- name: 部署监控 Agent
  hosts: all
  gather_facts: yes
  vars:
    node_exporter_version: "1.8.2"
    node_exporter_port: 9100
  tasks:
    - name: 创建监控用户
      user:
        name: node_exporter
        system: yes
        shell: /sbin/nologin

    - name: 下载 node_exporter
      unarchive:
        src: "https://github.com/prometheus/node_exporter/releases/download/v{{ node_exporter_version }}/node_exporter-{{ node_exporter_version }}.linux-amd64.tar.gz"
        dest: /tmp
        remote_src: yes

    - name: 安装二进制
      copy:
        src: "/tmp/node_exporter-{{ node_exporter_version }}.linux-amd64/node_exporter"
        dest: /usr/local/bin/node_exporter
        mode: "0755"
        remote_src: yes

    - name: 创建 systemd unit
      copy:
        dest: /etc/systemd/system/node_exporter.service
        content: |
          [Unit]
          Description=Node Exporter
          After=network.target

          [Service]
          User=node_exporter
          ExecStart=/usr/local/bin/node_exporter --web.listen-address=:{{ node_exporter_port }}
          Restart=always

          [Install]
          WantedBy=multi-user.target

    - name: 启动 node_exporter
      systemd:
        name: node_exporter
        enabled: yes
        state: started`,
		},
		{
			ID: "ssh-hardening", Name: "SSH 安全加固", Category: "安全",
			Description: "禁用 root 登录、禁用密码认证、配置密钥登录、修改端口、安装 Fail2Ban",
			Content: `---
- name: SSH 安全加固
  hosts: all
  gather_facts: yes
  vars:
    ssh_port: 22
    permit_root_login: "no"
    password_authentication: "no"
    allowed_users: []
  tasks:
    - name: 备份 sshd_config
      copy:
        src: /etc/ssh/sshd_config
        dest: /etc/ssh/sshd_config.bak
        remote_src: yes

    - name: 配置 sshd
      lineinfile:
        path: /etc/ssh/sshd_config
        regexp: "{{ item.regexp }}"
        line: "{{ item.line }}"
        state: present
      loop:
        - { regexp: "^#?Port", line: "Port {{ ssh_port }}" }
        - { regexp: "^#?PermitRootLogin", line: "PermitRootLogin {{ permit_root_login }}" }
        - { regexp: "^#?PasswordAuthentication", line: "PasswordAuthentication {{ password_authentication }}" }
        - { regexp: "^#?PubkeyAuthentication", line: "PubkeyAuthentication yes" }
        - { regexp: "^#?ChallengeResponseAuthentication", line: "ChallengeResponseAuthentication no" }
        - { regexp: "^#?UseDNS", line: "UseDNS no" }
      notify: restart sshd

    - name: 允许用户登录
      lineinfile:
        path: /etc/ssh/sshd_config
        line: "AllowUsers {{ allowed_users | join(' ') }}"
        state: present
      when: allowed_users | length > 0
      notify: restart sshd

    - name: 安装 Fail2Ban
      yum:
        name: fail2ban
        state: present

    - name: 配置 Fail2Ban SSH
      copy:
        dest: /etc/fail2ban/jail.local
        content: |
          [sshd]
          enabled = true
          port = {{ ssh_port }}
          maxretry = 3
          bantime = 3600
          findtime = 600

    - name: 启动 fail2ban
      systemd:
        name: fail2ban
        enabled: yes
        state: started

  handlers:
    - name: restart sshd
      systemd:
        name: sshd
        state: restarted`,
		},
		{
			ID: "lvm-extend", Name: "LVM 扩容", Category: "存储",
			Description: "新磁盘初始化 PV/VG/LV、扩容已有 LV、XFS 在线扩容",
			Content: `---
- name: LVM 管理
  hosts: all
  gather_facts: yes
  vars:
    disks: []
    volume_groups: {}
    logical_volumes: {}
  tasks:
    - name: 安装 lvm2
      yum:
        name: lvm2
        state: present

    - name: 创建 PV
      pv:
        disk: "{{ item }}"
        state: present
      loop: "{{ disks }}"

    - name: 创建/扩展现有 VG
      vg:
        vg: "{{ item.key }}"
        pvs: "{{ item.value.pvs | join(',') }}"
        state: present
      loop: "{{ volume_groups | dict2items }}"

    - name: 创建/扩展 LV
      lvol:
        vg: "{{ item.value.vg }}"
        lv: "{{ item.key }}"
        size: "{{ item.value.size }}"
        state: present
        resizefs: yes
      loop: "{{ logical_volumes | dict2items }}"

    - name: 挂载 LV
      mount:
        path: "{{ item.value.mount }}"
        src: "/dev/{{ item.value.vg }}/{{ item.key }}"
        fstype: "{{ item.value.fstype | default('xfs') }}"
        opts: defaults
        state: mounted
      loop: "{{ logical_volumes | dict2items }}"
      when: item.value.mount is defined`,
		},
		{
			ID: "install-nginx", Name: "安装 Nginx", Category: "应用",
			Description: "安装 Nginx、配置 SSL 反向代理、日志轮转",
			Content: `---
- name: 安装 Nginx
  hosts: all
  gather_facts: yes
  vars:
    nginx_port: 80
    nginx_ssl_port: 443
    server_name: "_"
    upstream: []
    ssl_cert: /etc/nginx/ssl/server.crt
    ssl_key: /etc/nginx/ssl/server.key
  tasks:
    - name: 安装 EPEL
      yum:
        name: epel-release
        state: present

    - name: 安装 Nginx
      yum:
        name: nginx
        state: present

    - name: 创建 SSL 目录
      file:
        path: /etc/nginx/ssl
        state: directory
        mode: "0700"

    - name: 生成自签名证书
      command: |
        openssl req -x509 -nodes -days 3650 \
          -subj "/CN={{ server_name }}" \
          -keyout {{ ssl_key }} \
          -out {{ ssl_cert }}
      args:
        creates: "{{ ssl_cert }}"

    - name: 配置反向代理
      copy:
        dest: /etc/nginx/conf.d/{{ server_name }}.conf
        content: |
          upstream backend {
              {% for u in upstream %}
              server {{ u }};
              {% endfor %}
          }

          server {
              listen {{ nginx_port }};
              server_name {{ server_name }};

              location / {
                  proxy_pass http://backend;
                  proxy_set_header Host $host;
                  proxy_set_header X-Real-IP $remote_addr;
              }
          }

          server {
              listen {{ nginx_ssl_port }} ssl;
              server_name {{ server_name }};

              ssl_certificate {{ ssl_cert }};
              ssl_certificate_key {{ ssl_key }};

              location / {
                  proxy_pass http://backend;
                  proxy_set_header Host $host;
                  proxy_set_header X-Real-IP $remote_addr;
              }
          }

    - name: 配置日志轮转
      copy:
        dest: /etc/logrotate.d/nginx
        content: |
          /var/log/nginx/*.log {
              daily
              rotate 30
              compress
              delaycompress
              missingok
              notifempty
              sharedscripts
              postrotate
                  /bin/kill -USR1 $(cat /var/run/nginx.pid 2>/dev/null) 2>/dev/null || true
              endscript
          }

    - name: 启动 Nginx
      systemd:
        name: nginx
        enabled: yes
        state: started`,
		},
		{
			ID: "deploy-app", Name: "应用部署", Category: "应用",
			Description: "通用部署流程：拉代码 → 安装依赖 → 编译打包 → 同步文件 → 重启服务",
			Content: `---
- name: 应用部署
  hosts: all
  gather_facts: yes
  vars:
    app_name: "myapp"
    git_repo: ""
    git_branch: "main"
    app_dir: "/opt/{{ app_name }}"
    app_user: "{{ app_name }}"
    service_file: ""
    build_command: ""
    sync_files: []
    env_vars: {}
  tasks:
    - name: 创建应用用户
      user:
        name: "{{ app_user }}"
        system: yes
        shell: /sbin/nologin
        home: "{{ app_dir }}"

    - name: 创建目录
      file:
        path: "{{ item }}"
        state: directory
        mode: "0755"
      loop:
        - "{{ app_dir }}"
        - "{{ app_dir }}/logs"

    - name: 克隆代码
      git:
        repo: "{{ git_repo }}"
        dest: "{{ app_dir }}/src"
        version: "{{ git_branch }}"
        force: yes
      when: git_repo != ""

    - name: 执行构建
      shell:
        cmd: "{{ build_command }}"
        chdir: "{{ app_dir }}/src"
      when: build_command != ""
      notify: restart {{ app_name }}

    - name: 同步配置文件
      copy:
        src: "{{ item.src }}"
        dest: "{{ item.dest }}"
        mode: "{{ item.mode | default('0644') }}"
      loop: "{{ sync_files }}"
      notify: restart {{ app_name }}

    - name: 写入环境变量
      lineinfile:
        path: "{{ app_dir }}/.env"
        line: "{{ item.key }}={{ item.value }}"
        create: yes
      loop: "{{ env_vars | dict2items }}"

    - name: 安装 systemd 服务
      copy:
        src: "{{ service_file }}"
        dest: "/etc/systemd/system/{{ app_name }}.service"
      when: service_file != ""
      notify: restart {{ app_name }}

  handlers:
    - name: restart {{ app_name }}
      systemd:
        name: "{{ app_name }}"
        state: restarted
        daemon_reload: yes`,
		},
	}
}

func (m *Manager) ListTemplates() []Template {
	tmpls := BuiltinTemplates()
	sort.Slice(tmpls, func(i, j int) bool {
		if tmpls[i].Category == tmpls[j].Category {
			return tmpls[i].Name < tmpls[j].Name
		}
		return tmpls[i].Category < tmpls[j].Category
	})
	return tmpls
}

func (m *Manager) GetTemplate(id string) *Template {
	for _, t := range BuiltinTemplates() {
		if t.ID == id {
			return &t
		}
	}
	return nil
}

func (m *Manager) CreateFromTemplate(tmplID, newID string) (*Playbook, error) {
	t := m.GetTemplate(tmplID)
	if t == nil {
		return nil, fmt.Errorf("模板 %s 不存在", tmplID)
	}
	p := &Playbook{
		ID:      newID,
		Name:    newID,
		Content: t.Content,
	}
	if err := m.SavePlaybook(p); err != nil {
		return nil, err
	}
	return p, nil
}
