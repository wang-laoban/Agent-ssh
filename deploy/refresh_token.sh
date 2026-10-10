sudo systemctl restart test-agent.service
sudo journalctl -u test-agent.service -n 20 --no-pager