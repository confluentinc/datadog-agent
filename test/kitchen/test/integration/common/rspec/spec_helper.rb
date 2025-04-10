require 'json'
require 'open-uri'
require 'rspec'
require 'rbconfig'
require 'yaml'
require 'find'
require 'tempfile'

#
# this enables RSpec output so that individual tests ("it behaves like...") are
# logged.
RSpec.configure do |c|
  c.default_formatter = "documentation"
end

os_cache = nil

# We retrieve the value defined in kitchen.yml because there is no simple way
# to set env variables on the target machine or via parameters in Kitchen/Busser
# See https://github.com/test-kitchen/test-kitchen/issues/662 for reference
def get_agent_flavor
  if os == :windows
    dna_json_path = "#{ENV['USERPROFILE']}\\AppData\\Local\\Temp\\kitchen\\dna.json"
  else
    dna_json_path = "/tmp/kitchen/dna.json"
  end
  JSON.parse(IO.read(dna_json_path)).fetch('dd-agent-rspec').fetch('agent_flavor')
end

def get_service_name(flavor)
  # Return the service name of the given flavor depending on the OS
  if os == :windows
    case flavor
    when "datadog-agent", "datadog-heroku-agent", "datadog-iot-agent"
      "datadogagent"
    when "datadog-dogstatsd"
      # Placeholder, not used yet
      "dogstatsd"
    end
  else
    case flavor
    when "datadog-agent", "datadog-heroku-agent", "datadog-iot-agent"
      "datadog-agent"
    when "datadog-dogstatsd"
      "datadog-dogstatsd"
    end
  end
end

def os
  # OS Detection from https://stackoverflow.com/questions/11784109/detecting-operating-systems-in-ruby
  os_cache ||= (
    host_os = RbConfig::CONFIG['host_os']
    case host_os
    when /mswin|msys|mingw|cygwin|bccwin|wince|emc/
      :windows
    when /darwin|mac os/
      :macosx
    when /linux/
      :linux
    when /solaris|bsd/
      :unix
    else
      raise Error::WebDriverError, "unknown os: #{host_os.inspect}"
    end
  )
end

def safe_program_files
  # HACK: on non-English Windows, Chef wrongly installs its 32-bit version on 64-bit hosts because
  # of this issue: https://github.com/chef/mixlib-install/issues/343
  # Because of this, the ENV['ProgramFiles'] content is wrong (it's `C:/Program Files (x86)`)
  # while the Agent is installed in `C:/Program Files`
  # To prevent this issue, we check the system arch and the ProgramFiles folder, and we fix it
  # if needed.

  # Env variables are frozen strings, they need to be duplicated to modify them
  program_files = ENV['ProgramFiles'].dup
  arch = `Powershell -command "(Get-WmiObject Win32_OperatingSystem).OsArchitecture"`
  if arch.include? "64" and program_files.include? "(x86)"
    program_files.slice!("(x86)")
    program_files.strip!
  end

  program_files
end


def agent_command
  if os == :windows
    '"C:\\Program Files\\Datadog\\Datadog Agent\\bin\\agent.exe"'
  else
    "sudo datadog-agent"
  end
end

def wait_until_service_stopped(service, timeout = 60)
  # Check if the service has stopped every second
  # Timeout after the given number of seconds
  for _ in 1..timeout do
    if !is_service_running?(service)
      case service
      when "datadog-agent"
        break if !is_port_bound(5001)
      when "datadog-dogstatsd"
        break if !is_port_bound(8125)
      else
        break
      end
    end
    sleep 1
  end
end

def wait_until_service_started(service, timeout = 30)
  # Check if the service has started every second
  # Timeout after the given number of seconds
  for _ in 1..timeout do
    if is_service_running?(service)
      case service
      when "datadog-agent"
        break if is_port_bound(5001)
      when "datadog-dogstatsd"
        break if is_port_bound(8125)
      else
        break
      end
    end
    sleep 1
  end
end

def stop(flavor)
  service = get_service_name(flavor)
  if os == :windows
    # forces the trace agent (and other dependent services) to stop
    result = system "net stop /y #{service} 2>&1"
    sleep 5
  else
    if has_systemctl
      result = system "sudo systemctl stop #{service}.service"
    elsif has_upstart
      result = system "sudo initctl stop #{service}"
    else
      result = system "sudo /sbin/service #{service} stop"
    end
  end
  wait_until_service_stopped(service)
  result
end

def start(flavor)
  service = get_service_name(flavor)
  if os == :windows
    result = system "net start #{service} 2>&1"
    sleep 5
  else
    if has_systemctl
      result = system "sudo systemctl start #{service}.service"
    elsif has_upstart
      result = system "sudo initctl start #{service}"
    else
      result = system "sudo /sbin/service #{service} start"
    end
  end
  wait_until_service_started(service)
  result
end

def restart(flavor)
  service = get_service_name(flavor)
  if os == :windows
    # forces the trace agent (and other dependent services) to stop
    if is_service_running?(service)
      result = system "net stop /y #{service} 2>&1"
      sleep 5
      wait_until_service_stopped(service)
    end
    result = system "net start #{service} 2>&1"
    sleep 5
    wait_until_service_started(service)
  else
    if has_systemctl
      result = system "sudo systemctl restart #{service}.service"
      # Worst case: the Agent has already stopped and restarted when we check if the process has been stopped
      # and we lose 5 seconds.
      wait_until_service_stopped(service, 5)
      wait_until_service_started(service, 5)
    elsif has_upstart
      # initctl can't restart
      result = system "(sudo initctl restart #{service} || sudo initctl start #{service})"
      wait_until_service_stopped(service, 5)
      wait_until_service_started(service, 5)
    else
      result = system "sudo /sbin/service #{service} restart"
      wait_until_service_stopped(service, 5)
      wait_until_service_started(service, 5)
    end
  end
  result
end

def has_systemctl
  system('command -v systemctl 2>&1 > /dev/null')
end

def has_upstart
  system('/sbin/init --version 2>&1 | grep -q upstart >/dev/null')
end

def has_dpkg
  system('command -v dpkg 2>&1 > /dev/null')
end

def info
  `#{agent_command} status 2>&1`
end

def integration_install(package)
  `#{agent_command} integration install -r #{package} 2>&1`.tap do |output|
    raise "Failed to install integrations package '#{package}' - #{output}" unless $? == 0
  end
end

def integration_remove(package)
  `#{agent_command} integration remove -r #{package} 2>&1`.tap do |output|
    raise "Failed to remove integrations package '#{package}' - #{output}" unless $? == 0
  end
end

def integration_freeze
  `#{agent_command} integration freeze 2>&1`.tap do |output|
    raise "Failed to get integrations freeze - #{output}" unless $? == 0
  end
end

def json_info
  tmpfile = Tempfile.new('agent-status')
  begin
    `#{agent_command} status -j -o #{tmpfile.path}`
    info_output = File.read(tmpfile.path)

    JSON.parse(info_output)
  ensure
    tmpfile.close
    tmpfile.unlink
  end
end

def windows_service_status(service)
  raise "windows_service_status is only for windows" unless os == :windows
  # Language-independent way of getting the service status
  return (`powershell -command "try { (get-service "#{service}" -ErrorAction Stop).Status } catch { write-host "NOTINSTALLED" }"`).upcase.strip
end

def is_service_running?(service)
  if os == :windows
    return windows_service_status(service) == "RUNNING"
  else
    if has_systemctl
      system "sudo systemctl status --no-pager #{service}.service"
    elsif has_upstart
      status = `sudo initctl status #{service}`
      status.include?('start/running')
    else
      status = `sudo /sbin/service #{service} status`
      status.include?('running')
    end
  end
end

def is_windows_service_installed(service)
  raise "is_windows_service_installed is only for windows" unless os == :windows
  return windows_service_status(service) != "NOTINSTALLED"
end

def is_flavor_running?(flavor)
  is_service_running?(get_service_name(flavor))
end

def is_process_running?(pname)
  if os == :windows
    tasklist = `tasklist /fi \"ImageName eq #{pname}\" 2>&1`
    if tasklist.include?(pname)
      return true
    end
  else
    return true if system("pgrep -f #{pname}")
  end
  return false
end

def agent_processes_running?
  %w(datadog-agent agent.exe).each do |p|
    return true if is_process_running?(p)
  end
  false
end

def dogstatsd_processes_running?
  %w(dogstatsd dogstatsd.exe).each do |p|
      return true if is_process_running?(p)
  end
  false
end

def read_agent_file(path, commit_hash)
  open("https://raw.githubusercontent.com/DataDog/datadog-agent/#{commit_hash}/#{path}").read()
end

# Hash of the commit the Agent was built from
def agent_git_hash
  JSON.parse(IO.read("/opt/datadog-agent/version-manifest.json"))['software']['datadog-agent']['locked_version']
end

def trace_agent_git_hash
  JSON.parse(IO.read("/opt/datadog-agent/version-manifest.json"))['software']['datadog-trace-agent']['locked_version']
end

# From a pip-requirements-formatted string, return a hash of 'dep_name' => 'version'
def read_requirements(file_contents)
  reqs = Hash.new
  file_contents.lines.reject do |line|
    /^#/ === line  # reject comment lines
  end.collect do |line|
    /(.+)==([^\s]+)/.match(line)
  end.compact.each do |match|
    reqs[match[1].downcase] = match[2]
  end
  reqs
end

def is_port_bound(port)
  if os == :windows
    port_regex = Regexp.new(port.to_s)
    port_regex.match(`netstat -n -b -a -p TCP 2>&1`)
  else
    # If netstat is not found (eg. on SUSE >= 15), use ss to get the list of ports used.
    system("sudo netstat -lntp | grep #{port} 1>/dev/null") || system("sudo ss -lntp | grep #{port} 1>/dev/null")
  end
end


def read_conf_file
    conf_path = ""
    if os == :windows
      conf_path = "#{ENV['ProgramData']}\\Datadog\\datadog.yaml"
    else
      conf_path = '/etc/datadog-agent/datadog.yaml'
    end
    puts "cp is #{conf_path}"
    f = File.read(conf_path)
    confYaml = YAML.load(f)
    confYaml
end

def fetch_python_version(timeout = 15)
  # Fetch the python_version from the Agent status
  # Timeout after the given number of seconds
  for _ in 1..timeout do
    json_info_output = json_info
    if json_info_output.key?('python_version') &&
      ! json_info_output['python_version'].nil? && # nil is considered a correct version by Gem::Version
      Gem::Version.correct?(json_info_output['python_version']) # Check that we do have a version number
        return json_info_output['python_version']
    end
    sleep 1
  end
  return nil
end

def is_file_signed(fullpath)
  puts "checking file #{fullpath}"
  expect(File).to exist(fullpath)
  output = `powershell -command "(get-authenticodesignature -FilePath '#{fullpath}').SignerCertificate.Thumbprint"`
  signature_hash = "33ACB4126192A96253EBF0616F222844E0E3EF0D"
  if output.upcase.strip == signature_hash.upcase.strip
    return true
  end

  puts("expected hash = #{signature_hash}, msi's hash = #{output}")
  return false
end

def is_dpkg_package_installed(package)
  system("dpkg -l #{package} | grep ii")
end

shared_examples_for 'Agent install' do
  it_behaves_like 'an installed Agent'
  it_behaves_like 'an installed Datadog Signing Keys'
end

shared_examples_for 'Agent behavior' do
  it_behaves_like 'a running Agent with no errors'
  it_behaves_like 'a running Agent with APM'
  it_behaves_like 'a running Agent with APM manually disabled'
  it_behaves_like 'an Agent with python3 enabled'
  it_behaves_like 'an Agent with integrations'
  it_behaves_like 'an Agent that stops'
  it_behaves_like 'an Agent that restarts'
end

shared_examples_for 'Agent uninstall' do
  it_behaves_like 'an Agent that is removed'
end

shared_examples_for "an installed Agent" do
  wait_until_service_started get_service_name("datadog-agent")

  it 'has an example config file' do
    if os != :windows
      expect(File).to exist('/etc/datadog-agent/datadog.yaml.example')
    end
  end

  it 'has a datadog-agent binary in usr/bin' do
    if os != :windows
      expect(File).to exist('/usr/bin/datadog-agent')
    end
  end

  # We retrieve the value defined in kitchen.yml because there is no simple way
  # to set env variables on the target machine or via parameters in Kitchen/Busser
  # See https://github.com/test-kitchen/test-kitchen/issues/662 for reference
  let(:skip_windows_signing_check) {
    if os == :windows
      dna_json_path = "#{ENV['USERPROFILE']}\\AppData\\Local\\Temp\\kitchen\\dna.json"
    else
      dna_json_path = "/tmp/kitchen/dna.json"
    end
    JSON.parse(IO.read(dna_json_path)).fetch('dd-agent-rspec').fetch('skip_windows_signing_test')
  }

  it 'is properly signed' do
    puts "skipping windows signing check #{skip_windows_signing_check}" if os == :windows and skip_windows_signing_check
    #puts "is an upgrade is #{is_upgrade}"
    if os == :windows and !skip_windows_signing_check
      # The user in the yaml file is "datadog", however the default test kitchen user is azure.
      # This allows either to be used without changing the test.
      msi_path = "#{ENV['USERPROFILE']}\\AppData\\Local\\Temp\\kitchen\\cache\\ddagent-cli.msi"
      msi_path_upgrade = "#{ENV['USERPROFILE']}\\AppData\\Local\\Temp\\kitchen\\cache\\ddagent-up.msi"

      # The upgrade file should only be present when doing an upgrade test.  Therefore,
      # check the file we're upgrading to, not the file we're upgrading from
      if File.file?(msi_path_upgrade)
        msi_path = msi_path_upgrade
      end
      is_signed = is_file_signed(msi_path)
      expect(is_signed).to be_truthy

      program_files = safe_program_files
      verify_signature_files = [
        # TODO: Uncomment this when we start shipping the security agent on Windows
        # "#{program_files}\\DataDog\\Datadog Agent\\bin\\agent\\security-agent.exe",
        "#{program_files}\\DataDog\\Datadog Agent\\bin\\agent\\process-agent.exe",
        "#{program_files}\\DataDog\\Datadog Agent\\bin\\agent\\trace-agent.exe",
        "#{program_files}\\DataDog\\Datadog Agent\\bin\\agent\\ddtray.exe",
        "#{program_files}\\DataDog\\Datadog Agent\\bin\\libdatadog-agent-three.dll",
        "#{program_files}\\DataDog\\Datadog Agent\\bin\\agent.exe",
        "#{program_files}\\DataDog\\Datadog Agent\\embedded3\\python.exe",
        "#{program_files}\\DataDog\\Datadog Agent\\embedded3\\pythonw.exe",
        "#{program_files}\\DataDog\\Datadog Agent\\embedded3\\python3.dll",
        "#{program_files}\\DataDog\\Datadog Agent\\embedded3\\python38.dll"
      ]
      libdatadog_agent_two = "#{program_files}\\DataDog\\Datadog Agent\\bin\\libdatadog-agent-two.dll"
      if File.file?(libdatadog_agent_two)
        verify_signature_files += [
          libdatadog_agent_two,
          "#{program_files}\\DataDog\\Datadog Agent\\embedded2\\python.exe",
          "#{program_files}\\DataDog\\Datadog Agent\\embedded2\\pythonw.exe",
          "#{program_files}\\DataDog\\Datadog Agent\\embedded2\\python27.dll"
        ]
      end

      verify_signature_files.each do |vf|
        expect(is_file_signed(vf)).to be_truthy
      end
    end
  end
end

shared_examples_for "an installed Datadog Signing Keys" do
  it 'is installed (on Debian-based systems)' do
    skip if os == :windows
    skip unless has_dpkg
    # Only check on Debian-based systems, which have dpkg installed
    expect(is_dpkg_package_installed('datadog-signing-keys')).to be_truthy
  end
end

shared_examples_for "a running Agent with no errors" do
  it 'has an agent binary' do
    if os != :windows
      expect(File).to exist('/usr/bin/datadog-agent')
    end
  end

  it 'is running' do
    expect(is_flavor_running? "datadog-agent").to be_truthy
  end

  it 'has a config file' do
    if os == :windows
      conf_path = "#{ENV['ProgramData']}\\Datadog\\datadog.yaml"
    else
      conf_path = '/etc/datadog-agent/datadog.yaml'
    end
    expect(File).to exist(conf_path)
  end

  it 'has running checks' do
    result = false
    # Wait for the collector to do its first run
    # Timeout after 30 seconds
    for _ in 1..30 do
      json_info_output = json_info
      if json_info_output.key?('runnerStats') &&
        json_info_output['runnerStats'].key?('Checks') &&
        !json_info_output['runnerStats']['Checks'].empty?
        result = true
        break
      end
      sleep 1
    end
    expect(result).to be_truthy
  end

  it 'has an info command' do
    # On systems that use systemd (on which the `start` script returns immediately)
    # sleep a few seconds to let the collector finish its first run
    # Windows seems to frequently have this same issue
    if os != :windows
      system('command -v systemctl 2>&1 > /dev/null && sleep 5')
    else
      sleep 5
    end

    expect(info).to include "Forwarder"
    expect(info).to include "DogStatsD"
    expect(info).to include "Host Info"
  end

  it 'has no errors in the info command' do
    info_output = info
    # The api key is invalid. this test ensures there are no other errors
    info_output = info_output.gsub "[ERROR] API Key is invalid" "API Key is invalid"
    expect(info_output).to_not include 'ERROR'
  end
end

shared_examples_for "a running Agent with APM" do
  it 'is bound to the port that receives traces by default' do
    expect(is_port_bound(8126)).to be_truthy
  end
end

shared_examples_for "a running Agent with APM manually disabled" do
  it 'is not bound to the port that receives traces when apm_enabled is set to false' do
    conf_path = ""
    if os != :windows
      conf_path = "/etc/datadog-agent/datadog.yaml"
    else
      conf_path = "#{ENV['ProgramData']}\\Datadog\\datadog.yaml"
    end

    f = File.read(conf_path)
    confYaml = YAML.load(f)
    if !confYaml.key("apm_config")
      confYaml["apm_config"] = {}
    end
    confYaml["apm_config"]["enabled"] = false
    File.write(conf_path, confYaml.to_yaml)

    output = restart "datadog-agent"
    if os != :windows
      expect(output).to be_truthy
      system 'command -v systemctl 2>&1 > /dev/null || sleep 5 || true'
    else
      sleep 5
    end
    expect(is_port_bound(8126)).to be_falsey
  end

  it "doesn't say 'not running' in the info command" do
    # Until it runs the logs agent by default it will say this
    # expect(info).to_not include 'not running'
  end
end

shared_examples_for 'an Agent that stops' do
  it 'stops' do
    output = stop "datadog-agent"
    if os != :windows
      expect(output).to be_truthy
    end
    expect(is_flavor_running? "datadog-agent").to be_falsey
  end

  it 'has connection refuse in the info command' do
    if os == :windows
      expect(info).to include 'No connection could be made'
    else
      expect(info).to include 'connection refuse'
    end
  end

  it 'is not running any agent processes' do
    expect(agent_processes_running?).to be_falsey
  end

  it 'starts after being stopped' do
    output = start "datadog-agent"
    if os != :windows
      expect(output).to be_truthy
    end
    expect(is_flavor_running? "datadog-agent").to be_truthy
  end
end

shared_examples_for 'an Agent that restarts' do
  it 'restarts when the agent is running' do
    if !is_flavor_running? "datadog-agent"
      start "datadog-agent"
    end
    output = restart "datadog-agent"
    if os != :windows
      expect(output).to be_truthy
    end
    expect(is_flavor_running? "datadog-agent").to be_truthy
  end

  it 'restarts when the agent is not running' do
    if is_flavor_running? "datadog-agent"
      stop "datadog-agent"
    end
    output = restart "datadog-agent"
    if os != :windows
      expect(output).to be_truthy
    end
    expect(is_flavor_running? "datadog-agent").to be_truthy
  end
end

shared_examples_for 'an Agent with python3 enabled' do
  it 'restarts after python_version is set to 3' do
    conf_path = ""
    if os != :windows
      conf_path = "/etc/datadog-agent/datadog.yaml"
    else
      conf_path = "#{ENV['ProgramData']}\\Datadog\\datadog.yaml"
    end
    f = File.read(conf_path)
    confYaml = YAML.load(f)
    confYaml["python_version"] = 3
    File.write(conf_path, confYaml.to_yaml)

    output = restart "datadog-agent"
    expect(output).to be_truthy
  end

  it 'runs Python 3 after python_version is set to 3' do
    result = false
    python_version = fetch_python_version
    if ! python_version.nil? && Gem::Version.new('3.0.0') <= Gem::Version.new(python_version)
      result = true
    end
    expect(result).to be_truthy
  end

  it 'restarts after python_version is set back to 2' do
    skip if info.include? "v7."
    conf_path = ""
    if os != :windows
      conf_path = "/etc/datadog-agent/datadog.yaml"
    else
      conf_path = "#{ENV['ProgramData']}\\Datadog\\datadog.yaml"
    end
    f = File.read(conf_path)
    confYaml = YAML.load(f)
    confYaml["python_version"] = 2
    File.write(conf_path, confYaml.to_yaml)

    output = restart "datadog-agent"
    expect(output).to be_truthy
  end

  it 'runs Python 2 after python_version is set back to 2' do
    skip if info.include? "v7."
    result = false
    python_version = fetch_python_version
    if ! python_version.nil? && Gem::Version.new('3.0.0') > Gem::Version.new(python_version)
      result = true
    end
    expect(result).to be_truthy
  end
end

shared_examples_for 'an Agent with integrations' do
  let(:integrations_freeze_file) do
    if os == :windows
      'C:\Program Files\Datadog\Datadog Agent\requirements-agent-release.txt'
    else
      '/opt/datadog-agent/requirements-agent-release.txt'
    end
  end

  before do
    freeze_content = File.read(integrations_freeze_file)
    freeze_content.gsub!(/datadog-cilium==.*/, 'datadog-cilium==1.5.3')
    File.write(integrations_freeze_file, freeze_content)

    integration_remove('datadog-cilium')
  end

  it 'can uninstall an installed package' do
    integration_install('datadog-cilium==1.5.3')

    expect do
      integration_remove('datadog-cilium')
    end.to change { integration_freeze.match?(%r{datadog-cilium==.*}) }.from(true).to(false)
  end

  it 'can install a new package' do
    integration_remove('datadog-cilium')

    expect do
      integration_install('datadog-cilium==1.5.3')
    end.to change { integration_freeze.match?(%r{datadog-cilium==1\.5\.3}) }.from(false).to(true)
  end

  it 'can upgrade an installed package' do
    expect do
      integration_install('datadog-cilium==1.6.0')
    end.to change { integration_freeze.match?(%r{datadog-cilium==1\.6\.0}) }.from(false).to(true)
  end

  it 'can downgrade an installed package' do
    integration_remove('datadog-cilium')
    integration_install('datadog-cilium==1.6.0')

    expect do
      integration_install('datadog-cilium==1.5.3')
    end.to change { integration_freeze.match?(%r{datadog-cilium==1\.5\.3}) }.from(false).to(true)
  end

  it 'cannot downgrade an installed package to a version older than the one shipped with the agent' do
    integration_remove('datadog-cilium')
    integration_install('datadog-cilium==1.5.3')

    expect do
      integration_install('datadog-cilium==1.5.2')
    end.to raise_error(/Failed to install integrations package 'datadog-cilium==1\.5\.2'/)
  end
end

shared_examples_for 'an Agent that is removed' do
  it 'should remove the agent' do
    if os == :windows
      # uninstallcmd = "start /wait msiexec /q /x 'C:\\Users\\azure\\AppData\\Local\\Temp\\kitchen\\cache\\ddagent-cli.msi'"
      uninstallcmd='for /f "usebackq" %n IN (`wmic product where "name like \'datadog%\'" get IdentifyingNumber ^| find "{"`) do start /wait msiexec /log c:\\uninst.log /q /x %n'
      expect(system(uninstallcmd)).to be_truthy
    else
      if system('which apt-get &> /dev/null')
        expect(system("sudo apt-get -q -y remove #{get_agent_flavor} > /dev/null")).to be_truthy
      elsif system('which yum &> /dev/null')
        expect(system("sudo yum -y remove #{get_agent_flavor} > /dev/null")).to be_truthy
      elsif system('which zypper &> /dev/null')
        expect(system("sudo zypper --non-interactive remove #{get_agent_flavor} > /dev/null")).to be_truthy
      else
        raise 'Unknown package manager'
      end
    end
  end

  it 'should not be running the agent after removal' do
    sleep 5
    expect(agent_processes_running?).to be_falsey
  end

  if os == :windows
    it 'should not make changes to system files' do
      exclude = [
            'C:/Windows/Assembly/Temp/',
            'C:/Windows/Assembly/Tmp/',
            'C:/windows/AppReadiness/',
            'C:/Windows/Temp/',
            'C:/Windows/Prefetch/',
            'C:/Windows/Installer/',
            'C:/Windows/WinSxS/',
            'C:/Windows/Logs/',
            'C:/Windows/servicing/',
            'c:/Windows/System32/catroot2/',
            'c:/windows/System32/config/',
            'C:/Windows/ServiceProfiles/NetworkService/AppData/Local/Microsoft/Windows/DeliveryOptimization/Logs/',
            'C:/Windows/ServiceProfiles/NetworkService/AppData/Local/Microsoft/Windows/DeliveryOptimization/Cache/',
            'C:/Windows/SoftwareDistribution/DataStore/Logs/',
            'C:/Windows/System32/wbem/Performance/',
            'c:/windows/System32/LogFiles/',
            'c:/windows/SoftwareDistribution/',
            'c:/windows/ServiceProfiles/NetworkService/AppData/',
            'c:/windows/System32/Tasks/Microsoft/Windows/UpdateOrchestrator/',
            'c:/windows/System32/Tasks/Microsoft/Windows/Windows Defender/Windows Defender Scheduled Scan'
      ].each { |e| e.downcase! }

      # We don't really need to create this file since we consume it right afterwards, but it's useful for debugging
      File.open("c:/after-files.txt", "w") do |out|
        Find.find('c:/windows/').each { |f| out.puts(f) }
      end

      before_files = File.readlines('c:/before-files.txt').reject { |f| f.downcase.start_with?(*exclude) }
      after_files = File.readlines('c:/after-files.txt').reject { |f| f.downcase.start_with?(*exclude) }

      missing_files = before_files - after_files
      new_files = after_files - before_files

      puts "New files:"
      new_files.each { |f| puts(f) }

      puts "Missing files:"
      missing_files.each { |f| puts(f) }

      expect(missing_files).to be_empty
    end
  end

  it 'should remove the installation directory' do
    if os == :windows
      expect(File).not_to exist("C:\\Program Files\\Datadog\\Datadog Agent\\")
    else
      expect(File).not_to exist("/opt/datadog-agent/")
    end
  end

  if os != :windows
    it 'should remove the agent link from bin' do
      expect(File).not_to exist('/usr/bin/datadog-agent')
    end
  end
end

shared_examples_for 'an Agent with APM enabled' do
  it 'has apm enabled' do
    confYaml = read_conf_file()
    expect(confYaml).to have_key("apm_config")
    expect(confYaml["apm_config"]).to have_key("enabled")
    expect(confYaml["apm_config"]["enabled"]).to be_truthy
    expect(is_port_bound(8126)).to be_truthy
  end
  it 'has the apm agent running' do
    expect(is_process_running?("trace-agent.exe")).to be_truthy
    expect(is_service_running?("datadog-trace-agent")).to be_truthy
  end
end

shared_examples_for 'an Agent with logs enabled' do
  it 'has logs enabled' do
    confYaml = read_conf_file()
    expect(confYaml).to have_key("logs_config")
    expect(confYaml).to have_key("logs_enabled")
    expect(confYaml["logs_enabled"]).to be_truthy
  end
end

shared_examples_for 'an Agent with process enabled' do
  it 'has process enabled' do
    confYaml = read_conf_file()
    expect(confYaml).to have_key("process_config")
    expect(confYaml["process_config"]).to have_key("process_collection")
    expect(confYaml["process_config"]["process_collection"]).to have_key("enabled")
    expect(confYaml["process_config"]["process_collection"]["enabled"]).to be_truthy
  end
  it 'has the process agent running' do
    expect(is_process_running?("process-agent.exe")).to be_truthy
    expect(is_service_running?("datadog-process-agent")).to be_truthy
  end
end

shared_examples_for 'an upgraded Agent with the expected version' do
  # We retrieve the value defined in kitchen.yml because there is no simple way
  # to set env variables on the target machine or via parameters in Kitchen/Busser
  # See https://github.com/test-kitchen/test-kitchen/issues/662 for reference
  let(:agent_expected_version) {
    if os == :windows
      dna_json_path = "#{ENV['USERPROFILE']}\\AppData\\Local\\Temp\\kitchen\\dna.json"
    else
      dna_json_path = "/tmp/kitchen/dna.json"
    end
    JSON.parse(IO.read(dna_json_path)).fetch('dd-agent-upgrade-rspec').fetch('agent_expected_version')
  }

  it 'runs with the expected version (based on the `info` command output)' do
    agent_short_version = /(\.?\d)+/.match(agent_expected_version)[0]
    expect(info).to include "v#{agent_short_version}"
  end

  it 'runs with the expected version (based on the version manifest file)' do
    if os == :windows
      version_manifest_file = "C:/Program Files/Datadog/Datadog Agent/version-manifest.txt"
    else
      version_manifest_file = '/opt/datadog-agent/version-manifest.txt'
    end
    expect(File).to exist(version_manifest_file)
    # Match the first line of the manifest file
    expect(File.open(version_manifest_file) {|f| f.readline.strip}).to match "agent #{agent_expected_version}"
  end
end

def get_user_sid(uname)
  output = `powershell -command "(New-Object System.Security.Principal.NTAccount('#{uname}')).Translate([System.Security.Principal.SecurityIdentifier]).value"`.strip
  output
end

def get_sddl_for_object(name)
  cmd = "powershell -command \"get-acl -Path \\\"#{name}\\\" | format-list -Property sddl\""
  outp = `#{cmd}`.gsub("\n", "").gsub(" ", "")
  sddl = outp.gsub("/\s+/", "").split(":").drop(1).join(":").strip
  sddl
end

def equal_sddl?(left, right)
  # First, split the sddl into the ownership (user and group), and the dacl
  left_array = left.split("D:")
  right_array = right.split("D:")

  # compare the ownership & group.  Must be the same
  if left_array[0] != right_array[0]
    return false
  end
  left_dacl = left_array[1].scan(/(\([^)]*\))/)
  right_dacl = right_array[1].scan(/(\([^)]*\))/)


  # if they're different lengths, they're different
  if left_dacl.length != right_dacl.length
    return false
  end

  ## now need to break up the DACL list, because they may be listed in different
  ## orders... the order doesn't matter but the components should be the same.  So..

  left_dacl.each do |left_entry|
    found = false
    right_dacl.each do |right_entry|
      if left_entry == right_entry
        found = true
        right_dacl.delete(right_entry)
        break
      end
    end
    if !found
      return false
    end
  end
  return false if right_dacl.length != 0
  return true
end
def get_security_settings
  fname = "secout.txt"
  system "secedit /export /cfg  #{fname} /areas USER_RIGHTS"
  data = Hash.new

  utext = File.open(fname).read
  text = utext.unpack("v*").pack("U*")
  text.each_line do |line|
    next unless line.include? "="
    kv = line.strip.split("=")
    data[kv[0].strip] = kv[1].strip
  end
  #File::delete(fname)
  data
end

def check_has_security_right(data, k, name)
  right = data[k]
  unless right
    return false
  end
  rights = right.split(",")
  rights.each do |r|
    return true if r == name
  end
  false
end

def check_is_user_in_group(user, group)
  members = `net localgroup "#{group}"`
  members.split(/\n+/).each do |line|
    return true if line.strip == user
  end
  false
end

def get_username_from_tasklist(exename)
  # output of tasklist command is
  # Image Name  PID  Session Name  Session#  Mem Usage Status  User Name  CPU Time  Window Title
  output = `tasklist /v /fi "imagename eq #{exename}" /nh`.gsub("\n", "").gsub("NT AUTHORITY", "NT_AUTHORITY")

  # for the above, the system user comes out as "NT AUTHORITY\System", which confuses the split
  # below.  So special case it, and get rid of the space

  #username is fully qualified <domain>\username
  uname = output.split(' ')[7].partition('\\').last
  uname
end
