package runtime

// Recorded outputs of a real nginx. They live in a file of their own because
// they are data, and because their value lies in not being invented: this is
// the output of nginx 1.20.1 as packaged by Oracle Linux 9, the same host
// where `nginx -T` was measured to fail for an ordinary user.
const (
	outputDashV = `nginx version: nginx/1.20.1
built by gcc 11.2.1 20220127 (Red Hat 11.2.1-9) (GCC)
built with OpenSSL 3.0.1 14 Dec 2021
TLS SNI support enabled
configure arguments: --prefix=/usr/share/nginx --sbin-path=/usr/sbin/nginx --modules-path=/usr/lib64/nginx/modules --conf-path=/etc/nginx/nginx.conf --error-log-path=/var/log/nginx/error.log --http-log-path=/var/log/nginx/access.log --pid-path=/run/nginx.pid --lock-path=/run/lock/subsys/nginx --user=nginx --group=nginx --with-compat --with-debug --with-file-aio --with-http_auth_request_module --with-http_gunzip_module --with-http_gzip_static_module --with-http_image_filter_module=dynamic --with-http_realip_module --with-http_ssl_module --with-http_stub_status_module --with-http_sub_module --with-http_v2_module --with-http_xslt_module=dynamic --with-mail=dynamic --with-mail_ssl_module --with-pcre --with-pcre-jit --with-stream=dynamic --with-stream_ssl_module --with-cc-opt='-O2 -flto=auto -ffat-lto-objects -fexceptions -g' --with-ld-opt='-Wl,-z,relro -Wl,--as-needed'
`

	// An nginx.org build, where the paths are relative to the prefix.
	outputDashVRelativePaths = `nginx version: nginx/1.24.0
configure arguments: --prefix=/usr/local/nginx --conf-path=conf/nginx.conf --pid-path=logs/nginx.pid --with-http_ssl_module
`

	outputTestOK = `nginx: the configuration file /etc/nginx/nginx.conf syntax is ok
nginx: configuration file /etc/nginx/nginx.conf test is successful
`

	outputTestFailed = `nginx: [emerg] unknown directive "foo" in /etc/nginx/conf.d/a.conf:3
nginx: configuration file /etc/nginx/nginx.conf test failed
`

	outputTestWithWarning = `nginx: [warn] conflicting server name "example.com" on 0.0.0.0:80, ignored
nginx: the configuration file /etc/nginx/nginx.conf syntax is ok
nginx: configuration file /etc/nginx/nginx.conf test is successful
`

	// The case measured on the real host: an ordinary user cannot read the
	// configuration.
	outputNoPrivilege = `nginx: [alert] could not open error log file: open() "/var/log/nginx/error.log" failed (13: Permission denied)
nginx: [emerg] open() "/etc/nginx/nginx.conf" failed (13: Permission denied)
`

	outputDump = `# configuration file /etc/nginx/nginx.conf:
user nginx;
worker_processes auto;

http {
    include /etc/nginx/conf.d/*.conf;
}

# configuration file /etc/nginx/conf.d/site.conf:
server {
    listen 80;
    server_name example.com;
}
`
)
