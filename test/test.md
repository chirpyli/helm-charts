
测试环境：
```sh
kubectl get nodes -A -o wide
NAME    STATUS   ROLES           AGE    VERSION   INTERNAL-IP       EXTERNAL-IP   OS-IMAGE             KERNEL-VERSION      CONTAINER-RUNTIME
node1   Ready    worker          96d    v1.34.3   192.168.232.137   <none>        Ubuntu 24.04.3 LTS   7.0.0-31-generic    containerd://2.2.0
node2   Ready    worker          96d    v1.34.3   192.168.232.138   <none>        Ubuntu 24.04.3 LTS   7.0.0-28-generic    containerd://2.2.1
node3   Ready    worker          91d    v1.34.3   192.168.232.129   <none>        Ubuntu 24.04.3 LTS   7.0.0-31-generic    containerd://2.2.1
slpc    Ready    control-plane   105d   v1.34.2   192.168.232.128   <none>        Ubuntu 24.04.2 LTS   6.8.0-139-generic   containerd://1.7.27
```