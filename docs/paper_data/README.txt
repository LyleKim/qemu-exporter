qemu-exporter paper data — collected 2026-09-08T06:46:19+00:00 on node ip-10-0-1-129

VMs:
  fig-vm  = instance-00000004  uuid b00eecaa-8eb1-42c4-9ba7-3b643305db5f  m1.medium 4GiB/2vCPU  ubuntu-24.04  TCG
            constant load: 2x 'yes>/dev/null' ; guest sampler -> ttyS0 -> console.log (GUESTSTAT lines)
  test-vm = instance-00000001  cirros m1.tiny (idle, secondary)

Fig.1 (fig1_snapshot.txt): memory accounting snapshot, VMs idle-ish
Fig.3 run1 (fig3_run1*): cpu-hog replicas=4, HOG_ON..HOG_OFF per markers, load~7
       pressure x12.5, runqueue x10, libvirt cpu_time -3%, guest busy% flat / steal 0
Fig.3 run2 (fig3_run2*): cpu-hog replicas=8, load peaked 323 (pathological), pressure x122, runqueue x90, VM cpu -20%
       -> use run1 as main figure, run2 as dose-response sentence only
accuracy.txt : exporter cpu_usage vs virsh cpu.time, 126s window, rel err 0.001%
overhead.txt : 1.1 mCPU (0.11% of a core), RSS 8-10 MiB, 5s scrape
coverage.txt : Table 1 evidence (libvirt / cAdvisor / ours)
